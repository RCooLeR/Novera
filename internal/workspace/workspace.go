// Package workspace is the Wails service that owns all filesystem access for
// the open project. It enforces path containment, atomic writes, optimistic
// concurrency (stale-write detection), and UTF-8-safe truncation.
package workspace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"novera/internal/artifacts"
	"novera/internal/datatools"
	"novera/internal/jobs"
	"novera/internal/paths"
)

// markerRe matches task annotations in their canonical form only — the marker
// word immediately followed (optionally after spaces) by ":" or "(", e.g.
// "TODO:", "FIXME :", "HACK(jdoe):". Requiring that punctuation avoids flagging
// the bare words where they appear in prose, keyword lists, or this scanner's
// own source (which previously produced spurious "Problems" entries).
var markerRe = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX)\b\s*[:(]`)

const (
	// maxEditorBytes caps how much of a file we hand to the editor. Larger files
	// are flagged TooLarge so the UI shows a placeholder instead. This ceiling is
	// dictated by reality, not caution: the file is read fully into memory, sent
	// across the WebView2 bridge, and held by Monaco as a single JavaScript
	// string — and V8 caps a string at ~512 MiB, with Monaco already crawling
	// well before that. 64 MiB is the largest size that stays usable; multi-GB
	// files need a paged/streaming viewer, not the text editor.
	maxEditorBytes = 64 << 20 // 64 MiB
	// binarySniffBytes is how many leading bytes we inspect for NUL to classify
	// a file as binary.
	binarySniffBytes = 8000
)

// ErrStale indicates the on-disk file changed since the editor last read it.
var ErrStale = errors.New("file changed on disk since it was last read")

// Info describes the currently open workspace.
type Info struct {
	Root   string `json:"root"`
	Name   string `json:"name"`
	IsOpen bool   `json:"isOpen"`
}

// Entry is a single directory child, used to build the file tree lazily.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // slash-separated, relative to root
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // unix milliseconds
}

// FileContent is the result of reading a file for the editor.
type FileContent struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Revision  string `json:"revision"` // sha256 of on-disk bytes
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	TooLarge  bool   `json:"tooLarge"`
	Size      int64  `json:"size"`
	// Encoding is the detected on-disk text encoding (utf-8, utf-8-bom, utf-16le,
	// utf-16be, latin-1). Content is always UTF-8; this tells the UI when the file
	// was not UTF-8 so it can warn and round-trip the encoding on save.
	Encoding string `json:"encoding"`
}

// WriteResult is returned after a successful write and carries the new revision
// so the editor can keep its optimistic-concurrency token current.
type WriteResult struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

// SelfWriteNotifier lets the workspace announce a path it is about to write so a
// file-change watcher can suppress the resulting fs event as Novera's own echo
// rather than reporting it as an external change.
type SelfWriteNotifier interface{ Suppress(abs string) }

// Service is the bound Wails service.
type Service struct {
	mu        sync.RWMutex
	root      string
	selfWrite SelfWriteNotifier  // optional; set once at startup
	jobs      *jobs.Service      // optional; tracks long data-tool ops
	arts      *artifacts.Service // optional; registers data-tool outputs as artifacts

	tableMu    sync.Mutex
	tableCache *tableResult  // most-recent filtered/sorted table result, served paged
	browseCur  *browseCursor // open streaming reader for sequential browse paging
	tableIdx   *tableIndex   // sparse row->byte-offset index for fast random access
}

// tableIndex is a sparse index of a CSV/TSV file: the total data-row count plus
// the byte offset of every indexStep-th data row, so random windows can seek
// near the target instead of scanning from the start. XLSX has no offsets.
type tableIndex struct {
	key     string // rel|fileSig|delimiter
	header  []string
	delim   string
	sheet   string
	comma   rune
	rows    int64   // data rows (excludes the header)
	offsets []int64 // byte offset of data row i*indexStep (CSV/TSV only)
	step    int
}

const indexStep = 2000

// dropTableIndex clears the cached row index. Caller must hold tableMu.
func (s *Service) dropTableIndex() { s.tableIdx = nil }

// browseCursor keeps one streaming reader open between sequential browse pages so
// paging a large file doesn't re-open + re-skip from the start each time
// (borrowed from Quarry's window-continuation approach).
type browseCursor struct {
	key    string // rel|fileSig|delimiter
	offset int    // data rows already consumed past the header
	header []string
	it     *tableIter
}

// dropBrowseCursor closes and clears the browse cursor. Caller must hold tableMu.
func (s *Service) dropBrowseCursor() {
	if s.browseCur != nil {
		s.browseCur.it.close()
		s.browseCur = nil
	}
}

// New constructs the workspace service.
func New() *Service { return &Service{} }

// WireSelfWriteNotifier wires a watcher that should ignore Novera's own atomic
// saves. It is a package function (not a method) so Wails doesn't expose this
// internal wiring as a bound frontend call. Call once at startup before any write.
func WireSelfWriteNotifier(s *Service, n SelfWriteNotifier) { s.selfWrite = n }

// WireJobsAndArtifacts wires the jobs ledger and artifact registry so long
// data-tool operations show up as tracked jobs and their outputs are registered
// as artifacts (with lineage). Package function so it isn't bound to the UI.
// Call once at startup.
func WireJobsAndArtifacts(s *Service, j *jobs.Service, a *artifacts.Service) {
	s.jobs = j
	s.arts = a
}

// runTracked runs a long data-tool op as a job visible in the Jobs panel. The op
// is synchronous within the bound call; streaming ops aren't cancel-wired yet, so
// the job carries no cancel hook.
func (s *Service) runTracked(kind, title string, fn func() error) error {
	if s.jobs == nil {
		return fn()
	}
	id := s.jobs.Start(kind, title, nil)
	err := fn()
	if err != nil {
		s.jobs.Finish(id, jobs.StatusFailed, err.Error())
	} else {
		s.jobs.Finish(id, jobs.StatusSuccess, "")
	}
	return err
}

// registerArtifact best-effort records an output file as an artifact with
// lineage. Failures are ignored — artifact bookkeeping must never fail a tool.
func (s *Service) registerArtifact(kind, outRel string, sources []string) {
	if s.arts == nil || strings.TrimSpace(outRel) == "" {
		return
	}
	_, _ = s.arts.CreateArtifact(artifacts.Artifact{
		Kind:    kind,
		Title:   filepath.Base(outRel),
		Path:    outRel,
		Sources: sources,
		Tool:    "data-tools",
	})
}

// Root returns the current absolute workspace root (empty if none open). It is
// also shared with sibling services (e.g. git) that operate on the same root.
func (s *Service) Root() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.root
}

// Open sets the active workspace to dir after validating it is a directory.
func (s *Service) Open(dir string) (Info, error) {
	if dir == "" {
		return Info{}, errors.New("no directory provided")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Info{}, err
	}
	// Canonicalise the root by resolving symlinks once, here. Everything
	// downstream (containment checks, watch-event matching) then compares
	// against a stable, fully-resolved root instead of a path that might point
	// through a symlink.
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Info{}, err
	}
	if !st.IsDir() {
		return Info{}, fmt.Errorf("%s is not a directory", abs)
	}
	s.mu.Lock()
	s.root = abs
	s.mu.Unlock()
	s.tableMu.Lock()
	s.tableCache = nil // don't carry a prior workspace's table result across opens
	s.dropBrowseCursor()
	s.dropTableIndex()
	s.tableMu.Unlock()
	return Info{Root: abs, Name: filepath.Base(abs), IsOpen: true}, nil
}

// Close clears the active workspace.
func (s *Service) Close() {
	s.mu.Lock()
	s.root = ""
	s.mu.Unlock()
	s.tableMu.Lock()
	s.tableCache = nil
	s.dropBrowseCursor()
	s.dropTableIndex()
	s.tableMu.Unlock()
}

// Current returns info about the open workspace.
func (s *Service) Current() Info {
	root := s.Root()
	if root == "" {
		return Info{}
	}
	return Info{Root: root, Name: filepath.Base(root), IsOpen: true}
}

// ListDir returns the immediate children of rel ("" or "." for the root),
// directories first then files, each sorted case-insensitively.
func (s *Service) ListDir(rel string) ([]Entry, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return nil, err
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(dirents))
	for _, de := range dirents {
		childAbs := filepath.Join(abs, de.Name())
		childRel, err := paths.Rel(root, childAbs)
		if err != nil {
			continue
		}
		info, err := de.Info()
		var size, modMs int64
		if err == nil {
			size = info.Size()
			modMs = info.ModTime().UnixMilli()
		}
		entries = append(entries, Entry{
			Name:    de.Name(),
			Path:    childRel,
			IsDir:   de.IsDir(),
			Size:    size,
			ModTime: modMs,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // dirs first
		}
		return lessFold(entries[i].Name, entries[j].Name)
	})
	return entries, nil
}

const maxAllFiles = 8000

// noiseDirs are skipped when walking the WHOLE tree — quick-open, workspace
// search, the Problems/diagnostics scan, and (transitively) the agent's file
// tools, which all delegate to those walks. The UI file tree (ListDir) is
// single-level and deliberately does NOT consult this set, so the explorer
// still shows everything.
//
// Entries are dependency, build-output, cache, tmp, and VCS-metadata
// directories across ecosystems. Collision-prone names that often hold real
// source (Debug, Release, packages, deps, env, …) are intentionally excluded:
// skipping a dir silently hides its files from search/diagnostics/the agent,
// so the set stays conservative.
var noiseDirs = map[string]bool{
	// VCS metadata
	".git": true, ".svn": true, ".hg": true, ".bzr": true,
	// JS/TS deps, caches, build output
	"node_modules": true, "bower_components": true, "jspm_packages": true,
	".yarn": true, ".pnpm-store": true, ".pnp": true, ".cache": true,
	".next": true, ".nuxt": true, ".svelte-kit": true, ".angular": true,
	".astro": true, ".docusaurus": true, ".gatsby": true, ".vite": true,
	".webpack": true, ".rollup.cache": true, ".parcel-cache": true, ".turbo": true,
	".fusebox": true, ".vercel": true, ".netlify": true, ".wrangler": true,
	".output": true, ".expo": true, ".serverless": true, ".firebase": true,
	".dynamodb": true, "_site": true, ".jekyll-cache": true, ".sass-cache": true,
	"elm-stuff": true,
	// generic build/output
	"dist": true, "build": true, "out": true, "bin": true, "obj": true,
	// Go / Rust / C / native
	"vendor": true, "target": true, ".cargo": true, ".build": true,
	"cmake-build-debug": true, "cmake-build-release": true, ".pio": true,
	".gocache": true, ".gotmp": true,
	// JVM / Scala
	".gradle": true, ".m2": true, ".sbt": true, ".ivy2": true,
	".bloop": true, ".metals": true, ".bsp": true, ".stack-work": true, ".dub": true,
	// Python
	"__pycache__": true, ".venv": true, "venv": true, ".tox": true, ".nox": true,
	".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true,
	".hypothesis": true, ".ipynb_checkpoints": true, ".eggs": true,
	// PHP / Ruby / Dart / Elixir
	".phpunit.cache": true, ".bundle": true, ".dart_tool": true, ".pub-cache": true,
	"_build": true, ".elixir_ls": true,
	// IDE / editor / tooling caches
	".idea": true, ".vs": true, ".history": true, ".scannerwork": true,
	// infra
	".terraform": true, ".terragrunt-cache": true, ".vagrant": true,
	// coverage / test artifacts
	"coverage": true, ".nyc_output": true, "TestResults": true,
	"BenchmarkDotNet.Artifacts": true,
	// iOS / macOS
	"Pods": true, "DerivedData": true, "Carthage": true,
	// temp
	"tmp": true, "temp": true, ".tmp": true,
}

// ListAllFiles returns slash-relative paths of every file under the workspace
// (skipping heavy/build directories), capped. Powers quick-open and search.
func (s *Service) ListAllFiles() ([]string, error) {
	root := s.Root()
	if root == "" {
		return []string{}, nil
	}
	out := make([]string, 0, 256)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if path != root && noiseDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never read through a symlink — it could escape the workspace
		}
		rel, relErr := paths.Rel(root, path)
		if relErr != nil {
			return nil
		}
		out = append(out, rel)
		if len(out) >= maxAllFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return out, nil
}

const (
	maxSearchMatches   = 2000
	maxSearchFileBytes = 1 << 20
	maxMatchLineRunes  = 400
)

// SearchMatch is one matching line.
type SearchMatch struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`   // 1-based
	Column int    `json:"column"` // 1-based, rune index
	Text   string `json:"text"`
}

// SearchResult is the outcome of a workspace text search.
type SearchResult struct {
	Matches   []SearchMatch `json:"matches"`
	FileCount int           `json:"fileCount"`
	Truncated bool          `json:"truncated"`
}

// Search does a literal substring search across workspace files (skipping
// heavy/build dirs, binaries, and oversized files), returning capped matches.
func (s *Service) Search(query string, caseSensitive bool) (SearchResult, error) {
	root := s.Root()
	q := strings.TrimSpace(query)
	result := SearchResult{Matches: []SearchMatch{}}
	if root == "" || q == "" {
		return result, nil
	}
	needle := q
	if !caseSensitive {
		needle = strings.ToLower(q)
	}
	files := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && noiseDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never read through a symlink — it could escape the workspace
		}
		if len(result.Matches) >= maxSearchMatches {
			return filepath.SkipAll
		}
		if info, e := d.Info(); e != nil || info.Size() > maxSearchFileBytes {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil || isBinary(data) {
			return nil
		}
		rel, e := paths.Rel(root, path)
		if e != nil {
			return nil
		}
		lineNo := 0
		for _, line := range strings.Split(string(data), "\n") {
			lineNo++
			hay := line
			if !caseSensitive {
				hay = strings.ToLower(line)
			}
			idx := strings.Index(hay, needle)
			if idx < 0 {
				continue
			}
			text := strings.TrimRight(line, "\r")
			if r := []rune(text); len(r) > maxMatchLineRunes {
				text = string(r[:maxMatchLineRunes])
			}
			result.Matches = append(result.Matches, SearchMatch{
				Path: rel,
				Line: lineNo,
				// idx indexes hay (possibly lowercased); slicing hay is always
				// valid, whereas line[:idx] can split a rune / overrun when
				// case-folding changed the byte length.
				Column: utf8.RuneCountInString(hay[:idx]) + 1,
				Text:   text,
			})
			files[rel] = true
			if len(result.Matches) >= maxSearchMatches {
				result.Truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	result.FileCount = len(files)
	return result, nil
}

const maxDiagnostics = 2000

// Diagnostic is one static finding (a TODO-style marker or a merge conflict).
type Diagnostic struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"` // error | warning | info
	Kind     string `json:"kind"`     // TODO | FIXME | HACK | XXX | conflict
	Message  string `json:"message"`
}

// DiagnosticsResult is the outcome of a static workspace scan.
type DiagnosticsResult struct {
	Items     []Diagnostic `json:"items"`
	FileCount int          `json:"fileCount"`
	Truncated bool         `json:"truncated"`
}

// Diagnostics scans workspace text files for TODO/FIXME/HACK/XXX markers and
// merge-conflict markers. Deterministic, bounded, no compiler/network.
func (s *Service) Diagnostics() (DiagnosticsResult, error) {
	root := s.Root()
	result := DiagnosticsResult{Items: []Diagnostic{}}
	if root == "" {
		return result, nil
	}
	files := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && noiseDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never read through a symlink — it could escape the workspace
		}
		if len(result.Items) >= maxDiagnostics {
			return filepath.SkipAll
		}
		if info, e := d.Info(); e != nil || info.Size() > maxSearchFileBytes {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil || isBinary(data) {
			return nil
		}
		rel, e := paths.Rel(root, path)
		if e != nil {
			return nil
		}
		lineNo := 0
		for _, line := range strings.Split(string(data), "\n") {
			lineNo++
			start := strings.TrimLeft(line, " \t")
			// Only the unambiguous conflict markers (start/end) — never "======="
			// alone, which legitimately appears in Markdown/comments.
			if strings.HasPrefix(start, "<<<<<<<") || strings.HasPrefix(start, ">>>>>>>") {
				result.add(Diagnostic{Path: rel, Line: lineNo, Column: 1, Severity: "error", Kind: "conflict", Message: clipRunes(strings.TrimRight(line, "\r"), 200)}, files)
			} else if loc := markerRe.FindStringSubmatchIndex(line); loc != nil {
				// loc[2]:loc[3] is the marker word (capture group 1).
				kind := line[loc[2]:loc[3]]
				sev := "info"
				if kind == "FIXME" || kind == "XXX" {
					sev = "warning"
				}
				result.add(Diagnostic{
					Path:     rel,
					Line:     lineNo,
					Column:   utf8.RuneCountInString(line[:loc[2]]) + 1,
					Severity: sev,
					Kind:     kind,
					Message:  clipRunes(strings.TrimSpace(line[loc[2]:]), 200),
				}, files)
			}
			if len(result.Items) >= maxDiagnostics {
				result.Truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	result.FileCount = len(files)
	return result, nil
}

func (r *DiagnosticsResult) add(d Diagnostic, files map[string]bool) {
	r.Items = append(r.Items, d)
	files[d.Path] = true
}

func clipRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

const (
	maxTableCols = 60
	// tablePageMax bounds how many rows one QueryTable call returns (payload cap).
	tablePageMax = 2000
	// tableResultCap bounds how many rows a filtered/sorted result holds in memory
	// (a sorted 2 GB file can't be fully materialised); beyond it, Capped is set.
	tableResultCap = 500_000
	// maxXlsxBytes guards the on-disk size of an XLSX (a zip, so a decompression
	// bomb risk). CSV/TSV are streamed and have no size cap.
	maxXlsxBytes = 50 << 20 // 50 MiB
)

// InferTableSchema infers a SQL-ish column schema for a CSV/TSV file from a
// bounded sample (column types, null counts, examples). A "Tools" utility and an
// agent tool both call this. delimiter "" auto-detects.
func (s *Service) InferTableSchema(rel, delimiter string) (datatools.SchemaResult, error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return datatools.SchemaResult{}, err
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".csv" && ext != ".tsv" {
		return datatools.SchemaResult{}, fmt.Errorf("schema inference supports CSV/TSV files, not %s", ext)
	}
	f, err := os.Open(abs)
	if err != nil {
		return datatools.SchemaResult{}, err
	}
	defer f.Close()
	sniff := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, sniff)
	comma, _ := resolveDelimiter(delimiter, ext, sniff[:n])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return datatools.SchemaResult{}, err
	}
	return datatools.InferCSVSchema(f, comma, 200) // reads ~200 rows then stops
}

// AnalyzeSQLDump inventories the tables and statements in a .sql/.dump file
// (streamed; safe on multi-GB dumps). Backs a "Tools" utility and an agent tool.
func (s *Service) AnalyzeSQLDump(rel string) (datatools.DumpSummary, error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return datatools.DumpSummary{}, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return datatools.DumpSummary{}, err
	}
	defer f.Close()
	return datatools.AnalyzeSQLDump(f, 0)
}

// DumpExtractResult reports the outputs of a dump extract/split.
type DumpExtractResult struct {
	Outputs []string `json:"outputs"` // workspace-relative output paths
	Tables  int      `json:"tables"`
	Bytes   int64    `json:"bytes"`
}

// prepCsvConvert resolves a CSV/TSV input, sniffs its delimiter, and infers the
// column names + SQL types (from a sample) for CSV→SQL generation.
func (s *Service) prepCsvConvert(rel string) (abs string, comma rune, names, types []string, err error) {
	abs, err = paths.Resolve(s.Root(), rel)
	if err != nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".csv" && ext != ".tsv" {
		err = fmt.Errorf("CSV→SQL supports CSV/TSV files, not %s", ext)
		return
	}
	f, e := os.Open(abs)
	if e != nil {
		err = e
		return
	}
	sniff := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, sniff)
	comma, _ = resolveDelimiter("", ext, sniff[:n])
	_, _ = f.Seek(0, io.SeekStart)
	sch, e := datatools.InferCSVSchema(f, comma, 200)
	_ = f.Close()
	if e != nil {
		err = e
		return
	}
	names = make([]string, len(sch.Columns))
	types = make([]string, len(sch.Columns))
	for i, c := range sch.Columns {
		names[i], types[i] = c.Name, c.Type
	}
	return
}

// PreviewCsvToSql returns the SQL (CREATE TABLE + INSERTs) for the first rows of
// a CSV/TSV file — a glance before writing the full output.
func (s *Service) PreviewCsvToSql(rel, tableName string, includeCreate bool) (string, error) {
	abs, comma, names, types, err := s.prepCsvConvert(rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opts := datatools.SQLConvertOptions{TableName: tableName, Comma: comma, Columns: names, ColumnTypes: types, IncludeCreate: includeCreate, BatchSize: 500}
	return datatools.PreviewCSVToSQL(f, opts, 50)
}

// ConvertCsvToSql streams a CSV/TSV into a SQL file (CREATE TABLE + INSERTs) at a
// workspace-relative output path.
func (s *Service) ConvertCsvToSql(rel, outRel, tableName string, includeCreate bool) (datatools.SQLConvertSummary, error) {
	var sum datatools.SQLConvertSummary
	err := s.runTracked("csv→sql", "CSV→SQL "+outRel, func() error {
		var e error
		sum, e = s.convertCsvToSqlImpl(rel, outRel, tableName, includeCreate)
		return e
	})
	if err == nil {
		s.registerArtifact("sql", outRel, []string{rel})
	}
	return sum, err
}

func (s *Service) convertCsvToSqlImpl(rel, outRel, tableName string, includeCreate bool) (datatools.SQLConvertSummary, error) {
	abs, comma, names, types, err := s.prepCsvConvert(rel)
	if err != nil {
		return datatools.SQLConvertSummary{}, err
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return datatools.SQLConvertSummary{}, err
	}
	in, err := os.Open(abs)
	if err != nil {
		return datatools.SQLConvertSummary{}, err
	}
	defer in.Close()
	out, err := os.Create(outAbs)
	if err != nil {
		return datatools.SQLConvertSummary{}, err
	}
	opts := datatools.SQLConvertOptions{TableName: tableName, Comma: comma, Columns: names, ColumnTypes: types, IncludeCreate: includeCreate, BatchSize: 500}
	sum, cerr := datatools.ConvertCSVToSQL(in, out, opts)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	return sum, cerr
}

// ExtractDumpTable copies one table's statements out of a SQL dump into outRel.
func (s *Service) ExtractDumpTable(rel, table, outRel string) (DumpExtractResult, error) {
	var res DumpExtractResult
	err := s.runTracked("dump-extract", "Extract "+table+" → "+outRel, func() error {
		var e error
		res, e = s.extractDumpTableImpl(rel, table, outRel)
		return e
	})
	if err == nil {
		s.registerArtifact("sql", outRel, []string{rel})
	}
	return res, err
}

func (s *Service) extractDumpTableImpl(rel, table, outRel string) (DumpExtractResult, error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return DumpExtractResult{}, err
	}
	sum, err := s.AnalyzeSQLDump(rel)
	if err != nil {
		return DumpExtractResult{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return DumpExtractResult{}, err
	}
	var rng *datatools.DumpTableRange
	for _, r := range datatools.PlanDumpRanges(sum, st.Size()) {
		if r.Name == table {
			rr := r
			rng = &rr
			break
		}
	}
	if rng == nil {
		return DumpExtractResult{}, fmt.Errorf("table %q not found in the dump", table)
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return DumpExtractResult{}, err
	}
	n, err := copyByteRange(abs, outAbs, rng.Start, rng.End)
	if err != nil {
		return DumpExtractResult{}, err
	}
	return DumpExtractResult{Outputs: []string{outRel}, Tables: 1, Bytes: n}, nil
}

// SplitDump writes each table in a SQL dump to outDirRel/<table>.sql.
func (s *Service) SplitDump(rel, outDirRel string) (DumpExtractResult, error) {
	var res DumpExtractResult
	err := s.runTracked("dump-split", "Split dump "+rel, func() error {
		var e error
		res, e = s.splitDumpImpl(rel, outDirRel)
		return e
	})
	// Register each per-table output (lineage = the source dump). Even on a
	// partial failure, the files written so far are real and worth tracking.
	for _, o := range res.Outputs {
		s.registerArtifact("sql", o, []string{rel})
	}
	return res, err
}

func (s *Service) splitDumpImpl(rel, outDirRel string) (DumpExtractResult, error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return DumpExtractResult{}, err
	}
	sum, err := s.AnalyzeSQLDump(rel)
	if err != nil {
		return DumpExtractResult{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return DumpExtractResult{}, err
	}
	res := DumpExtractResult{Outputs: []string{}}
	for _, r := range datatools.PlanDumpRanges(sum, st.Size()) {
		outRel := outDirRel + "/" + sanitizeFileName(r.Name) + ".sql"
		outAbs, err := s.prepOutput(outRel)
		if err != nil {
			return res, err
		}
		n, err := copyByteRange(abs, outAbs, r.Start, r.End)
		if err != nil {
			return res, err
		}
		res.Bytes += n
		res.Tables++
		res.Outputs = append(res.Outputs, outRel)
	}
	return res, nil
}

// TransformDump streams a SQL dump through cleanup presets (remove DEFINER,
// rewrite ENGINE/CHARSET, drop AUTO_INCREMENT, rename a database) into outRel.
func (s *Service) TransformDump(rel, outRel string, t datatools.DumpTransform) (datatools.DumpTransformSummary, error) {
	var sum datatools.DumpTransformSummary
	err := s.runTracked("dump-clean", "Clean dump → "+outRel, func() error {
		var e error
		sum, e = s.transformDumpImpl(rel, outRel, t)
		return e
	})
	if err == nil {
		s.registerArtifact("sql", outRel, []string{rel})
	}
	return sum, err
}

func (s *Service) transformDumpImpl(rel, outRel string, t datatools.DumpTransform) (datatools.DumpTransformSummary, error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return datatools.DumpTransformSummary{}, err
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return datatools.DumpTransformSummary{}, err
	}
	in, err := os.Open(abs)
	if err != nil {
		return datatools.DumpTransformSummary{}, err
	}
	defer in.Close()
	out, err := os.Create(outAbs)
	if err != nil {
		return datatools.DumpTransformSummary{}, err
	}
	sum, terr := datatools.TransformDump(in, out, t)
	if closeErr := out.Close(); terr == nil {
		terr = closeErr
	}
	return sum, terr
}

// csvComma resolves a CSV/TSV input and detects its delimiter.
func (s *Service) csvComma(rel string) (abs string, comma rune, err error) {
	abs, err = paths.Resolve(s.Root(), rel)
	if err != nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".csv" && ext != ".tsv" {
		err = fmt.Errorf("expected a CSV/TSV file, not %s", ext)
		return
	}
	f, e := os.Open(abs)
	if e != nil {
		err = e
		return
	}
	sniff := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, sniff)
	comma, _ = resolveDelimiter("", ext, sniff[:n])
	_ = f.Close()
	return
}

// ProjectCsv writes a new CSV/TSV keeping only the named columns, in order.
func (s *Service) ProjectCsv(rel, outRel string, columns []string) (int64, error) {
	abs, comma, err := s.csvComma(rel)
	if err != nil {
		return 0, err
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return 0, err
	}
	in, err := os.Open(abs)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(outAbs)
	if err != nil {
		return 0, err
	}
	n, cerr := datatools.ProjectCSV(in, out, comma, columns)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	return n, cerr
}

// AddCsvColumn writes a new CSV/TSV with a constant-valued column appended.
func (s *Service) AddCsvColumn(rel, outRel, name, value string) (int64, error) {
	abs, comma, err := s.csvComma(rel)
	if err != nil {
		return 0, err
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return 0, err
	}
	in, err := os.Open(abs)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(outAbs)
	if err != nil {
		return 0, err
	}
	n, cerr := datatools.AddCSVColumn(in, out, comma, name, value)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	return n, cerr
}

// DumpTableToCsv extracts a table's pg_dump COPY block into a CSV file at outRel.
func (s *Service) DumpTableToCsv(rel, table, outRel string) (int64, error) {
	var n int64
	err := s.runTracked("dump→csv", "Extract "+table+" → "+outRel, func() error {
		var e error
		n, e = s.dumpTableToCsvImpl(rel, table, outRel)
		return e
	})
	if err == nil {
		s.registerArtifact("dataset", outRel, []string{rel})
	}
	return n, err
}

func (s *Service) dumpTableToCsvImpl(rel, table, outRel string) (int64, error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return 0, err
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return 0, err
	}
	in, err := os.Open(abs)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(outAbs)
	if err != nil {
		return 0, err
	}
	rows, found, derr := datatools.DumpTableToCSV(in, table, out)
	if closeErr := out.Close(); derr == nil {
		derr = closeErr
	}
	if derr != nil {
		return rows, derr
	}
	if !found {
		return 0, fmt.Errorf("no COPY data block found for table %q (this works on pg_dump COPY dumps)", table)
	}
	return rows, nil
}

// prepOutput resolves a workspace-relative output path, verifies containment, and
// ensures its parent directory exists.
func (s *Service) prepOutput(outRel string) (string, error) {
	outAbs, err := paths.Resolve(s.Root(), outRel)
	if err != nil {
		return "", err
	}
	if err := assertContainedReal(s.Root(), outAbs); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(outAbs), 0o755); err != nil {
		return "", err
	}
	if s.selfWrite != nil {
		s.selfWrite.Suppress(outAbs)
	}
	return outAbs, nil
}

func copyByteRange(srcAbs, dstAbs string, start, end int64) (int64, error) {
	in, err := os.Open(srcAbs)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if _, err := in.Seek(start, io.SeekStart); err != nil {
		return 0, err
	}
	out, err := os.Create(dstAbs)
	if err != nil {
		return 0, err
	}
	n, cerr := io.CopyN(out, in, end-start)
	if cerr == io.EOF {
		cerr = nil
	}
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	return n, cerr
}

func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "table"
	}
	return out
}

// IsTabular reports whether rel is a viewable tabular file.
func IsTabular(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".csv", ".tsv", ".xlsx", ".xlsm":
		return true
	}
	return false
}

// TableQuery selects a window of a tabular file, optionally filtered/sorted over
// the WHOLE file. A blank filter and SortCol < 0 means plain windowed browsing
// (only the window is read off disk — no full scan).
type TableQuery struct {
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	Filter    string `json:"filter"`    // case-insensitive substring across cells; "" = none
	SortCol   int    `json:"sortCol"`   // -1 = no sort
	SortDir   int    `json:"sortDir"`   // 1 asc, -1 desc
	Delimiter string `json:"delimiter"` // "" = auto-detect; else comma|semicolon|tab|pipe (CSV/TSV)
}

// TablePage is one window of a tabular file.
type TablePage struct {
	Path      string     `json:"path"`
	Sheet     string     `json:"sheet"`
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Offset    int        `json:"offset"`
	HasMore   bool       `json:"hasMore"`   // more rows after this window (current filter/sort)
	TotalRows int        `json:"totalRows"` // -1 when unknown (browse mode); else matched count
	Capped    bool       `json:"capped"`    // the filtered/sorted set hit tableResultCap
	Delimiter string     `json:"delimiter"` // delimiter actually used (comma|semicolon|tab|pipe); "" for XLSX
	Message   string     `json:"message"`
}

// tableResult is a cached, fully-realised filtered/sorted result, served paged.
type tableResult struct {
	key     string
	sheet   string
	delim   string
	columns []string
	rows    [][]string
	capped  bool
}

// QueryTable returns a window of a CSV/TSV/XLSX file for the analytics grid. The
// first row is the header. Browsing reads only the requested window straight off
// disk; a filter or sort is applied across the entire file (the realised result
// is cached so paging through it doesn't rescan).
func (s *Service) QueryTable(rel string, q TableQuery) (TablePage, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return TablePage{}, err
	}
	limit := q.Limit
	if limit <= 0 || limit > tablePageMax {
		limit = tablePageMax
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	out := TablePage{Path: rel, Offset: q.Offset, TotalRows: -1, Columns: []string{}, Rows: [][]string{}}

	if ext := strings.ToLower(filepath.Ext(rel)); ext == ".xlsx" || ext == ".xlsm" {
		if st, e := os.Stat(abs); e == nil && st.Size() > maxXlsxBytes {
			out.Message = "Workbook is too large to preview."
			return out, nil
		}
	}

	if strings.TrimSpace(q.Filter) == "" && q.SortCol < 0 {
		return s.tableBrowse(abs, rel, q.Delimiter, q.Offset, limit, out)
	}

	// Filtering/sorting is a different access mode — release any browse cursor.
	s.tableMu.Lock()
	s.dropBrowseCursor()
	s.tableMu.Unlock()

	res, err := s.tableResultFor(abs, rel, q)
	if err != nil {
		return TablePage{}, err
	}
	out.Sheet = res.sheet
	out.Columns = res.columns
	out.Capped = res.capped
	out.Delimiter = res.delim
	total := len(res.rows)
	out.TotalRows = total
	start := q.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	out.Rows = res.rows[start:end]
	out.HasMore = end < total
	if total == 0 {
		out.Message = "No rows match the filter."
	}
	return out, nil
}

// tableBrowse streams just the [offset, offset+limit) window off disk (plus a
// one-row peek for HasMore); TotalRows stays -1 since we never scan the whole file.
func (s *Service) tableBrowse(abs, rel, delim string, offset, limit int, out TablePage) (TablePage, error) {
	key := rel + "|" + fileSig(abs) + "|" + delim

	s.tableMu.Lock()
	defer s.tableMu.Unlock()

	cur := s.browseCur
	if cur == nil || cur.key != key || cur.offset != offset {
		// Can't resume (different file/delimiter, or a non-sequential jump).
		s.dropBrowseCursor()
		var header []string
		var it *tableIter
		var skip int
		if idx := s.tableIdx; idx != nil && idx.key == key && len(idx.offsets) > 0 && offset > 0 {
			// Seek near the target using the sparse index, then skip the remainder
			// — a random jump no longer rescans from the start of the file.
			stepIdx := offset / idx.step
			if stepIdx >= len(idx.offsets) {
				stepIdx = len(idx.offsets) - 1
			}
			var err error
			it, err = openCSVIterAt(abs, idx.comma, idx.offsets[stepIdx])
			if err != nil {
				return TablePage{}, err
			}
			it.sheet, it.delim = idx.sheet, idx.delim
			header = idx.header
			skip = offset - stepIdx*idx.step
		} else {
			var err error
			header, it, err = openTableIter(abs, rel, delim)
			if err != nil {
				return TablePage{}, err
			}
			skip = offset
		}
		for i := 0; i < skip; i++ {
			_, ok, e := it.next()
			if e != nil {
				it.close()
				return TablePage{}, e
			}
			if !ok { // offset past EOF
				out.Sheet = it.sheet
				out.Delimiter = it.delim
				out.Columns = clampCols(header)
				it.close()
				return out, nil
			}
		}
		cur = &browseCursor{key: key, offset: offset, header: header, it: it}
		s.browseCur = cur
	}

	it := cur.it
	out.Sheet = it.sheet
	out.Delimiter = it.delim
	out.Columns = clampCols(cur.header)
	ncols := len(out.Columns)
	for len(out.Rows) < limit {
		rec, ok, e := it.next()
		if e != nil {
			s.dropBrowseCursor()
			return TablePage{}, e
		}
		if !ok {
			break
		}
		out.Rows = append(out.Rows, normalizeRow(rec, ncols))
	}
	cur.offset += len(out.Rows)
	if len(out.Rows) == limit {
		// A full page — assume more rows follow (the next page confirms). Avoids a
		// peek that would desync the resumable cursor position.
		out.HasMore = true
	} else {
		s.dropBrowseCursor() // hit EOF — release the held reader
	}
	if offset == 0 && len(out.Rows) == 0 && ncols == 0 {
		out.Message = "Empty file."
	}
	return out, nil
}

// fileSig is a cheap content-change token (size + mtime) for cache/cursor keys.
func fileSig(abs string) string {
	if st, e := os.Stat(abs); e == nil {
		return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	}
	return "?"
}

// TableInfoResult reports a tabular file's data-row count (for the virtual
// scrollbar) and whether a sparse offset index is available (fast random jumps).
type TableInfoResult struct {
	Rows    int64 `json:"rows"`    // data rows (excludes the header)
	Indexed bool  `json:"indexed"` // sparse byte-offset index built (CSV/TSV)
}

// TableInfo counts the data rows of a tabular file and (for CSV/TSV) builds a
// sparse row->byte-offset index, cached per file+delimiter. It's meant to be
// called in the background after a file opens: browsing works immediately and
// the true total / fast jumps light up once this returns.
func (s *Service) TableInfo(rel, delimiter string) (TableInfoResult, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return TableInfoResult{}, err
	}
	key := rel + "|" + fileSig(abs) + "|" + delimiter

	s.tableMu.Lock()
	if s.tableIdx != nil && s.tableIdx.key == key {
		idx := s.tableIdx
		s.tableMu.Unlock()
		return TableInfoResult{Rows: idx.rows, Indexed: len(idx.offsets) > 0}, nil
	}
	s.tableMu.Unlock()

	header, it, err := openTableIter(abs, rel, delimiter)
	if err != nil {
		return TableInfoResult{}, err
	}
	idx := &tableIndex{key: key, header: clampCols(header), delim: it.delim, sheet: it.sheet, step: indexStep}

	ext := strings.ToLower(filepath.Ext(rel))
	if ext == ".csv" || ext == ".tsv" {
		it.close() // header captured; the index scan re-reads via a byte scanner
		idx.comma, _ = resolveDelimiter(it.delim, ext, nil)
		rows, offsets, serr := scanCSVRows(abs)
		if serr != nil {
			return TableInfoResult{}, serr
		}
		idx.rows, idx.offsets = rows, offsets
	} else {
		// XLSX: count by streaming the already-open iterator (no random-seek index).
		var n int64
		for {
			_, ok, e := it.next()
			if e != nil {
				it.close()
				return TableInfoResult{}, e
			}
			if !ok {
				break
			}
			n++
		}
		it.close()
		idx.rows = n
	}

	s.tableMu.Lock()
	s.tableIdx = idx
	s.tableMu.Unlock()
	return TableInfoResult{Rows: idx.rows, Indexed: len(idx.offsets) > 0}, nil
}

// scanCSVRows counts data rows (records minus the header) and records the byte
// offset of every indexStep-th data row. It splits records with a small
// quote-aware state machine (a newline outside quotes ends a record), so a
// newline embedded in a quoted field doesn't create a false boundary.
func scanCSVRows(abs string) (rows int64, offsets []int64, err error) {
	f, err := os.Open(abs)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 256<<10)

	const (
		stNormal = iota
		stQuoted
		stQuoteMaybe // saw a '"' while quoted; could be end-quote or an escaped ""
	)
	state := stNormal
	var pos int64     // absolute byte position
	var records int64 // completed records (terminated by a newline)
	var pending bool  // bytes seen since the last record boundary
	buf := make([]byte, 256<<10)
	for {
		n, rerr := br.Read(buf)
		for i := 0; i < n; i++ {
			b := buf[i]
			if b != '\n' || state == stQuoted {
				pending = true
			}
			switch state {
			case stNormal:
				if b == '"' {
					state = stQuoted
				} else if b == '\n' {
					records++
					// The record that starts after this newline is data row
					// (records-1) when records>=1 (record 0 is the header).
					if dataRow := records - 1; dataRow >= 0 && dataRow%indexStep == 0 {
						offsets = append(offsets, pos+1)
					}
					pending = false
				}
			case stQuoted:
				if b == '"' {
					state = stQuoteMaybe
				}
			case stQuoteMaybe:
				switch b {
				case '"':
					state = stQuoted // escaped "" inside the quoted field
				case '\n':
					records++
					if dataRow := records - 1; dataRow >= 0 && dataRow%indexStep == 0 {
						offsets = append(offsets, pos+1)
					}
					pending = false
					state = stNormal
				default:
					state = stNormal
				}
			}
			pos++
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, nil, rerr
		}
	}
	totalRecords := records
	if pending {
		totalRecords++ // last record had no trailing newline
	}
	if totalRecords <= 1 {
		return 0, offsets, nil // header only (or empty)
	}
	return totalRecords - 1, offsets, nil
}

// openCSVIterAt opens a CSV/TSV reader positioned at byteOffset (a record start),
// for index-seeked random windows. No header is read at this position.
func openCSVIterAt(abs string, comma rune, byteOffset int64) (*tableIter, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(byteOffset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	r := csv.NewReader(bufio.NewReaderSize(f, 64<<10))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true
	next := func() ([]string, bool, error) {
		rec, e := r.Read()
		if e == io.EOF {
			return nil, false, nil
		}
		if e != nil {
			return nil, false, e
		}
		return rec, true, nil
	}
	return &tableIter{next: next, close: func() { _ = f.Close() }}, nil
}

// tableResultFor returns the whole-file filtered/sorted result for q, from cache
// when the file (mtime+size) and query are unchanged, else by streaming the file.
func (s *Service) tableResultFor(abs, rel string, q TableQuery) (*tableResult, error) {
	key := fmt.Sprintf("%s|%s|%s|%d|%d|%s", rel, fileSig(abs), q.Filter, q.SortCol, q.SortDir, q.Delimiter)

	s.tableMu.Lock()
	if s.tableCache != nil && s.tableCache.key == key {
		res := s.tableCache
		s.tableMu.Unlock()
		return res, nil
	}
	s.tableMu.Unlock()

	header, it, err := openTableIter(abs, rel, q.Delimiter)
	if err != nil {
		return nil, err
	}
	defer it.close()
	cols := clampCols(header)
	ncols := len(cols)
	filter := strings.ToLower(strings.TrimSpace(q.Filter))
	rows := [][]string{}
	capped := false
	for {
		rec, ok, e := it.next()
		if e != nil {
			return nil, e
		}
		if !ok {
			break
		}
		if filter != "" && !rowMatches(rec, ncols, filter) {
			continue
		}
		rows = append(rows, normalizeRow(rec, ncols))
		if len(rows) >= tableResultCap {
			capped = true
			break
		}
	}
	if q.SortCol >= 0 && q.SortCol < ncols {
		dir := q.SortDir
		if dir == 0 {
			dir = 1
		}
		col := q.SortCol
		sort.SliceStable(rows, func(i, j int) bool { return compareCells(rows[i][col], rows[j][col])*dir < 0 })
	}
	res := &tableResult{key: key, sheet: it.sheet, delim: it.delim, columns: cols, rows: rows, capped: capped}
	s.tableMu.Lock()
	s.tableCache = res
	s.tableMu.Unlock()
	return res, nil
}

// tableIter streams the records of a tabular file (header consumed separately).
type tableIter struct {
	next  func() ([]string, bool, error) // (record, ok, err); ok=false at EOF
	close func()
	sheet string
	delim string // resolved CSV/TSV delimiter name; "" for XLSX
}

// openTableIter opens a tabular file for streaming. For CSV/TSV, delim overrides
// the field separator ("comma"|"semicolon"|"tab"|"pipe"); "" auto-detects it
// from the first line.
func openTableIter(abs, rel, delim string) ([]string, *tableIter, error) {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".xlsx", ".xlsm":
		f, err := excelize.OpenFile(abs)
		if err != nil {
			return nil, nil, err
		}
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			_ = f.Close()
			return nil, nil, errors.New("workbook has no sheets")
		}
		sheet := sheets[0]
		rowIter, err := f.Rows(sheet)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		next := func() ([]string, bool, error) {
			if !rowIter.Next() {
				return nil, false, rowIter.Error()
			}
			cols, e := rowIter.Columns()
			if e != nil {
				return nil, false, e
			}
			return cols, true, nil
		}
		closer := func() { _ = rowIter.Close(); _ = f.Close() }
		header, ok, err := next()
		if err != nil {
			closer()
			return nil, nil, err
		}
		if !ok {
			header = nil
		}
		return header, &tableIter{next: next, close: closer, sheet: sheet}, nil
	case ".csv", ".tsv":
		f, err := os.Open(abs)
		if err != nil {
			return nil, nil, err
		}
		// Sniff the start to pick (or honour an override of) the delimiter, then
		// rewind so the reader sees the whole file.
		sniff := make([]byte, 64<<10)
		n, _ := io.ReadFull(f, sniff)
		comma, delimName := resolveDelimiter(delim, ext, sniff[:n])
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		r := csv.NewReader(bufio.NewReaderSize(f, 64<<10))
		r.Comma = comma
		r.FieldsPerRecord = -1 // tolerate ragged rows
		r.LazyQuotes = true
		r.ReuseRecord = true // the slice is reused each Read — callers copy before storing
		next := func() ([]string, bool, error) {
			rec, e := r.Read()
			if e == io.EOF {
				return nil, false, nil
			}
			if e != nil {
				return nil, false, e
			}
			return rec, true, nil
		}
		header, ok, err := next()
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if ok {
			header = append([]string(nil), header...) // copy out of the reused buffer
		} else {
			header = nil
		}
		return header, &tableIter{next: next, close: func() { _ = f.Close() }, delim: delimName}, nil
	}
	return nil, nil, fmt.Errorf("not a tabular file: %s", ext)
}

// delimiters maps a name to its rune, in tie-break preference order.
var delimiters = []struct {
	name string
	r    rune
}{{"comma", ','}, {"semicolon", ';'}, {"tab", '\t'}, {"pipe", '|'}}

// resolveDelimiter returns the field separator and its name. A recognized
// override wins; otherwise it's auto-detected from the first line of sniff,
// falling back to tab for .tsv and comma otherwise.
func resolveDelimiter(override, ext string, sniff []byte) (rune, string) {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "comma", ",":
		return ',', "comma"
	case "semicolon", ";":
		return ';', "semicolon"
	case "tab", "\t", "\\t":
		return '\t', "tab"
	case "pipe", "|":
		return '|', "pipe"
	}
	line := sniff
	if i := bytes.IndexByte(sniff, '\n'); i >= 0 {
		line = sniff[:i]
	}
	bestName, bestR, bestN := "", ' ', 0
	for _, d := range delimiters {
		if c := bytes.Count(line, []byte(string(d.r))); c > bestN {
			bestN, bestR, bestName = c, d.r, d.name
		}
	}
	if bestN == 0 {
		if ext == ".tsv" {
			return '\t', "tab"
		}
		return ',', "comma"
	}
	return bestR, bestName
}

func clampCols(header []string) []string {
	if len(header) > maxTableCols {
		header = header[:maxTableCols]
	}
	return append([]string{}, header...)
}

// normalizeRow copies rec into a fixed-width row (safe to store even when the
// reader reuses its record buffer).
func normalizeRow(rec []string, ncols int) []string {
	cells := make([]string, ncols)
	for i := 0; i < ncols && i < len(rec); i++ {
		cells[i] = rec[i]
	}
	return cells
}

func rowMatches(rec []string, ncols int, lowerFilter string) bool {
	for i := 0; i < ncols && i < len(rec); i++ {
		if strings.Contains(strings.ToLower(rec[i]), lowerFilter) {
			return true
		}
	}
	return false
}

var tableNumRe = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?$`)

// compareCells orders two cells numeric-aware (plain decimals only, so ids with
// leading zeros stay lexical), returning -1/0/1.
func compareCells(a, b string) int {
	an, aok := tableNumber(a)
	bn, bok := tableNumber(b)
	if aok && bok {
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

func tableNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !tableNumRe.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// maxChunkBytes caps a single large-file page so the read + bridge transfer
// stays bounded regardless of what the caller asks for.
const maxChunkBytes = 2 << 20 // 2 MiB

// FileChunk is one byte-range page of a file, for the read-only viewer that
// handles files too large for the editor. Only the requested window is read
// into memory — never the whole file.
type FileChunk struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"` // byte offset of this page
	Length int    `json:"length"` // bytes actually returned
	Total  int64  `json:"total"`  // total file size in bytes
	EOF    bool   `json:"eof"`    // this page reaches the end of the file
	Binary bool   `json:"binary"` // the file looks binary (sniffed from its head)
	Text   string `json:"text"`   // UTF-8 text page (empty when Binary)
	Hex    string `json:"hex"`    // canonical hex dump (only when Binary)
}

// ReadFileRange returns a single byte-range page of a file using ReadAt, so a
// multi-GB file can be inspected without loading it whole. Binary-ness is
// sniffed from the file head (not the page) so mid-file text pages aren't
// misclassified; binary files are returned as a hex dump.
func (s *Service) ReadFileRange(rel string, offset int64, length int) (FileChunk, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return FileChunk{}, err
	}
	if length <= 0 || length > maxChunkBytes {
		length = maxChunkBytes
	}
	f, err := os.Open(abs)
	if err != nil {
		return FileChunk{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return FileChunk{}, err
	}
	total := st.Size()
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}

	// Sniff binary-ness from the file head, independent of the requested page.
	head := make([]byte, binarySniffBytes)
	hn, _ := f.ReadAt(head, 0)
	binary := isBinary(head[:hn])

	buf := make([]byte, length)
	n, readErr := f.ReadAt(buf, offset)
	if readErr != nil && readErr != io.EOF {
		return FileChunk{}, readErr
	}
	buf = buf[:n]

	out := FileChunk{
		Path:   rel,
		Offset: offset,
		Length: n,
		Total:  total,
		EOF:    offset+int64(n) >= total,
		Binary: binary,
	}
	if binary {
		out.Hex = hex.Dump(buf)
	} else {
		out.Text = string(buf)
	}
	return out, nil
}

// ReadFile returns file content for the editor. Binary or oversized files are
// flagged and returned with empty content so the UI can render a placeholder.
func (s *Service) ReadFile(rel string) (FileContent, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return FileContent{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return FileContent{}, err
	}
	out := FileContent{Path: rel, Size: st.Size()}
	if st.Size() > maxEditorBytes {
		out.TooLarge = true
		// Derive the revision from the file's metadata rather than reading the
		// whole (potentially huge) file just to hash it — oversized files are
		// never editable through the editor, so a cheap stat token suffices.
		out.Revision = revisionOfStat(st)
		return out, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return FileContent{}, err
	}
	out.Revision = revisionOfBytes(data)
	// Decode known text encodings (UTF-8/BOM, UTF-16 LE/BE, Latin-1) so non-UTF-8
	// text opens readable instead of as a hex dump; only genuinely binary data is
	// flagged. Content is normalised to UTF-8; Encoding records the original.
	text, enc, ok := decodeText(data)
	if !ok {
		out.Binary = true
		return out, nil
	}
	out.Content = text
	out.Encoding = enc
	return out, nil
}

// WriteFile writes content atomically. If the file already exists and
// expectedRevision is non-empty, the on-disk revision must match or ErrStale is
// returned (optimistic concurrency). content is UTF-8; encoding (utf-8,
// utf-8-bom, utf-16le, utf-16be, latin-1; empty = utf-8) selects the on-disk
// encoding so a non-UTF-8 file round-trips. Returns the new revision.
func (s *Service) WriteFile(rel, content, expectedRevision, encoding string) (WriteResult, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return WriteResult{}, err
	}
	if err := assertContainedReal(root, abs); err != nil {
		return WriteResult{}, err
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		// File exists: optimistic-concurrency check (advisory — the read here and
		// the rename in writeAtomic are not one atomic operation).
		if expectedRevision != "" {
			current := revisionOfFile(abs)
			if current != expectedRevision {
				return WriteResult{}, ErrStale
			}
		}
	} else if expectedRevision != "" {
		// The editor expected a specific existing version but the file is gone
		// (deleted/moved out from under it). Deletion is a change too — treat it
		// as stale rather than silently resurrecting the file. An unconditional
		// create (expectedRevision == "") is still allowed to fall through.
		if os.IsNotExist(statErr) {
			return WriteResult{}, ErrStale
		}
		return WriteResult{}, statErr
	}
	// Tell the watcher this write is ours so it doesn't echo back as an external
	// change. Done just before the write so the suppression window covers the
	// atomic create/rename burst.
	if s.selfWrite != nil {
		s.selfWrite.Suppress(abs)
	}
	data, err := encodeText(content, encoding)
	if err != nil {
		return WriteResult{}, err
	}
	if err := writeAtomic(abs, data); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Path: rel, Revision: revisionOfBytes(data)}, nil
}

// ReadRaw returns a file's exact bytes (existed=false, nil data if absent). The
// rollback journal uses it to snapshot/restore content byte-faithfully —
// including binary and the original encoding — bypassing the editor's text decode.
func (s *Service) ReadRaw(rel string) (data []byte, existed bool, err error) {
	abs, err := paths.Resolve(s.Root(), rel)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}

// WriteRaw writes exact bytes atomically (path-contained, watcher-suppressed),
// without the editor's encoding/optimistic-revision handling. Used by the
// rollback restore and by append/copy where the payload is raw bytes.
func (s *Service) WriteRaw(rel string, data []byte) error {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return err
	}
	if err := assertContainedReal(root, abs); err != nil {
		return err
	}
	if s.selfWrite != nil {
		s.selfWrite.Suppress(abs)
	}
	return writeAtomic(abs, data)
}

// CreateFile creates an empty file (error if it already exists).
func (s *Service) CreateFile(rel string) (Entry, error) {
	return s.create(rel, false)
}

// CreateDir creates a directory (and any missing parents).
func (s *Service) CreateDir(rel string) (Entry, error) {
	return s.create(rel, true)
}

func (s *Service) create(rel string, dir bool) (Entry, error) {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return Entry{}, err
	}
	if _, err := os.Stat(abs); err == nil {
		return Entry{}, fmt.Errorf("%s already exists", rel)
	}
	if dir {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return Entry{}, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return Entry{}, err
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return Entry{}, err
		}
		_ = f.Close()
	}
	st, _ := os.Stat(abs)
	return Entry{Name: filepath.Base(abs), Path: rel, IsDir: dir, Size: sizeOf(st), ModTime: modOf(st)}, nil
}

// Rename moves fromRel to toRel within the workspace.
func (s *Service) Rename(fromRel, toRel string) error {
	root := s.Root()
	fromAbs, err := paths.Resolve(root, fromRel)
	if err != nil {
		return err
	}
	toAbs, err := paths.Resolve(root, toRel)
	if err != nil {
		return err
	}
	if err := assertContainedReal(root, fromAbs); err != nil {
		return err
	}
	if err := assertContainedReal(root, toAbs); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o755); err != nil {
		return err
	}
	return os.Rename(fromAbs, toAbs)
}

// Delete removes a file or directory (recursively) within the workspace.
func (s *Service) Delete(rel string) error {
	root := s.Root()
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return err
	}
	if abs == root {
		return errors.New("refusing to delete the workspace root")
	}
	if err := assertContainedReal(root, abs); err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

// --- helpers ---

// assertContainedReal re-checks, immediately before a mutating filesystem
// operation, that abs still resolves (after evaluating symlinks on its existing
// prefix) to a location inside root. paths.Resolve already guarantees lexical
// containment; this narrows the TOCTOU window in which a path component could be
// swapped for an out-of-root symlink between Resolve and the operation. It is
// best-effort defense in depth, not a substitute for openat-style fd pinning,
// and the optimistic-revision checks remain advisory.
func assertContainedReal(root, abs string) error {
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Target may not exist yet (creating/renaming-to); evaluate the nearest
		// existing ancestor instead.
		resolved, err = filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			return nil // can't resolve; let the operation itself fail if truly broken
		}
	}
	if _, err := paths.Rel(root, resolved); err != nil {
		return paths.ErrEscapesWorkspace
	}
	return nil
}

func writeAtomic(abs string, data []byte) error {
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".novera-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

func revisionOfBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func revisionOfFile(abs string) string {
	data, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	return revisionOfBytes(data)
}

// revisionOfStat is a cheap content-revision token for files we deliberately do
// not read in full (oversized). It is opaque and only compared for equality.
func revisionOfStat(st os.FileInfo) string {
	if st == nil {
		return ""
	}
	return fmt.Sprintf("stat:%d:%d", st.Size(), st.ModTime().UnixNano())
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > binarySniffBytes {
		n = binarySniffBytes
	}
	head := data[:n]
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	// Invalid UTF-8 signals binary — but only treat it as such for a genuinely
	// malformed head, not when we merely sliced through the final rune at the
	// sniff boundary. When the window was truncated (n < len(data)), trim up to
	// 3 trailing bytes (a UTF-8 rune is at most 4 bytes) and re-check; if a prefix
	// becomes valid, it was just a clipped final rune, so treat as text.
	if n > 0 && !utf8.Valid(head) {
		if n < len(data) {
			for k := 0; k < 3 && len(head) > 1; k++ {
				head = head[:len(head)-1]
				if utf8.Valid(head) {
					return false
				}
			}
		}
		return true
	}
	return false
}

func sizeOf(st os.FileInfo) int64 {
	if st == nil {
		return 0
	}
	return st.Size()
}

func modOf(st os.FileInfo) int64 {
	if st == nil {
		return 0
	}
	return st.ModTime().UnixMilli()
}

func lessFold(a, b string) bool {
	la, lb := len(a), len(b)
	for i := 0; i < la && i < lb; i++ {
		ca, cb := lowerASCII(a[i]), lowerASCII(b[i])
		if ca != cb {
			return ca < cb
		}
	}
	return la < lb
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
