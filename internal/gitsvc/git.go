// Package gitsvc is the Wails service backing the Source Control panel. It
// shells out to the system `git` (like a real IDE) but with hardened,
// non-interactive env and correct porcelain parsing (-z + core.quotepath=false)
// so filenames with spaces/non-ASCII are handled — a bug in the prior
// generation. Diffs are returned as old/new content so the frontend can render
// them with Monaco's diff editor (no fragile patch parsing).
package gitsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"novera/internal/paths"
)

const (
	statusTimeout   = 5 * time.Second
	diffTimeout     = 8 * time.Second
	mutationTimeout = 20 * time.Second
	maxDiffBytes    = 2 << 20 // 2 MiB per side before we mark the diff too large
)

// RootProvider yields the active workspace root (shared with the workspace svc).
type RootProvider interface{ Root() string }

// Service is the bound Wails git service.
type Service struct {
	roots RootProvider
}

// New constructs the git service sharing the workspace root provider.
func New(roots RootProvider) *Service { return &Service{roots: roots} }

// FileChange is one entry in the status lists.
type FileChange struct {
	Path     string `json:"path"`
	OldPath  string `json:"oldPath"`
	Index    string `json:"index"`    // staged status code
	Worktree string `json:"worktree"` // working-tree status code
	Summary  string `json:"summary"`  // modified|added|deleted|untracked|renamed|changed
	Staged   bool   `json:"staged"`
}

// Status is the repository snapshot rendered by the Source Control panel.
type Status struct {
	Available   bool         `json:"available"` // a repo is present and readable
	Branch      string       `json:"branch"`
	Head        string       `json:"head"`
	AheadBehind string       `json:"aheadBehind"`
	Staged      []FileChange `json:"staged"`
	Unstaged    []FileChange `json:"unstaged"`
	Message     string       `json:"message"`
}

// DiffContent carries both sides so Monaco's diff editor renders the change.
type DiffContent struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
	Binary  bool   `json:"binary"`
	Message string `json:"message"`
}

// CommitResult reports the outcome of a commit and the refreshed status.
type CommitResult struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
	Subject   string `json:"subject"`
	Message   string `json:"message"`
	Status    Status `json:"status"`
}

// Status returns the working-tree status, or Available=false with a message
// when there is no workspace / not a git repo.
func (s *Service) Status() (Status, error) {
	root := s.root()
	if root == "" {
		return Status{Message: "Open a folder to use source control."}, nil
	}
	if _, err := s.git(statusTimeout, "rev-parse", "--is-inside-work-tree"); err != nil {
		return Status{Message: repoUnavailableMessage(err)}, nil
	}
	branch := strings.TrimSpace(s.gitOK(statusTimeout, "branch", "--show-current"))
	if branch == "" {
		branch = "detached"
	}
	head := strings.TrimSpace(s.gitOK(statusTimeout, "rev-parse", "--short", "HEAD"))
	raw := s.gitOK(statusTimeout, "-c", "core.quotepath=false", "status", "--porcelain=v1", "-z", "--branch")
	changes, aheadBehind := parseStatusZ(raw)
	staged, unstaged := splitChanges(changes)
	msg := fmt.Sprintf("%s is clean.", branch)
	if len(changes) > 0 {
		msg = fmt.Sprintf("%s · %d changed file(s)", branch, len(changes))
	}
	return Status{
		Available:   true,
		Branch:      branch,
		Head:        head,
		AheadBehind: aheadBehind,
		Staged:      staged,
		Unstaged:    unstaged,
		Message:     msg,
	}, nil
}

// Diff returns the two sides of a change for the Monaco diff editor. When
// staged is true it shows HEAD vs the index (what `git diff --cached` shows);
// otherwise it shows the index vs the working tree (what `git diff` shows), so
// the staged and unstaged rows of one file render their distinct diffs instead
// of an identical HEAD-vs-worktree view.
func (s *Service) Diff(rel, oldRel string, staged bool) (DiffContent, error) {
	root := s.root()
	out := DiffContent{Path: rel}
	if root == "" {
		out.Message = "Open a folder first."
		return out, nil
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		out.Message = err.Error()
		return out, nil
	}
	// For renames, the HEAD side lives at the old path.
	headPath := clean
	if strings.TrimSpace(oldRel) != "" {
		if oldClean, oldErr := cleanRelPath(oldRel); oldErr == nil {
			headPath = oldClean
		}
	}

	// Defense in depth beyond cleanRelPath's blacklist: confirm both the
	// working-tree path and the (possibly renamed) HEAD path resolve INSIDE the
	// workspace via securejoin before either side is read.
	if _, rerr := paths.Resolve(root, clean); rerr != nil {
		out.Message = rerr.Error()
		return out, nil
	}
	if _, rerr := paths.Resolve(root, headPath); rerr != nil {
		out.Message = rerr.Error()
		return out, nil
	}

	var oldText, newText string
	if staged {
		// HEAD vs index. "HEAD:./" / ":./" are resolved relative to cwd so this
		// works whether the workspace is the repo root or a subdirectory.
		var err1, err2 error
		oldText, _, err1 = s.gitShow("HEAD:./" + headPath) // empty == newly added at HEAD (normal)
		newText, _, err2 = s.gitShow(":./" + clean)
		if errors.Is(err1, context.DeadlineExceeded) || errors.Is(err2, context.DeadlineExceeded) {
			out.Message = "Couldn't load the diff — git timed out. Try again."
			return out, nil
		}
	} else {
		// index vs working tree (fall back to HEAD when the path isn't staged).
		if idx, ok, _ := s.gitShow(":./" + clean); ok {
			oldText = idx
		} else if head, _, herr := s.gitShow("HEAD:./" + headPath); herr == nil {
			oldText = head
		} else if errors.Is(herr, context.DeadlineExceeded) {
			out.Message = "Couldn't load the previous version — git timed out. Try again."
			return out, nil
		}
		// Resolve the working-tree path through securejoin so a symlinked entry
		// can't make us read a file outside the workspace.
		abs, rerr := paths.Resolve(root, clean)
		if rerr != nil {
			return out, rerr
		}
		if data, readErr := os.ReadFile(abs); readErr == nil { // empty if deleted
			newText = string(data)
		}
	}

	if isBinary([]byte(oldText)) || isBinary([]byte(newText)) {
		out.Binary = true
		out.Message = "Binary file — diff not shown."
		return out, nil
	}
	if len(oldText) > maxDiffBytes || len(newText) > maxDiffBytes {
		return DiffContent{Path: rel, Message: "File too large to diff."}, nil
	}
	out.OldText = oldText
	out.NewText = newText
	return out, nil
}

// gitShow returns the content of a path at a git ref-spec (e.g. "HEAD:./x" for
// the committed version, ":./x" for the staged/index version), whether it exists
// there, and the underlying error (so a timeout can be told apart from a path
// that simply isn't present at that ref).
func (s *Service) gitShow(spec string) (string, bool, error) {
	out, err := s.git(diffTimeout, "show", spec)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

// UnifiedDiff returns a unified `git diff HEAD` for rel (or the whole worktree
// when rel is empty), capped. Used by the agent's git_diff tool and available
// to the UI.
func (s *Service) UnifiedDiff(rel string) (string, error) {
	if s.root() == "" {
		return "", errors.New("open a folder first")
	}
	// -c core.quotepath=false so non-ASCII paths in the diff header aren't
	// octal-escaped (matches the status parser).
	args := []string{"-c", "core.quotepath=false", "--no-pager", "diff", "HEAD"}
	if strings.TrimSpace(rel) != "" {
		clean, err := cleanRelPath(rel)
		if err != nil {
			return "", err
		}
		args = append(args, "--", clean)
	}
	out, err := s.git(diffTimeout, args...)
	if err != nil {
		return "", err
	}
	if len(out) > maxDiffBytes {
		// Back up to a rune boundary so truncation can't split a multi-byte
		// character and produce invalid UTF-8.
		cut := maxDiffBytes
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "\n…(diff truncated)"
	}
	return out, nil
}

// Stage adds a path to the index.
func (s *Service) Stage(rel string) (Status, error) { return s.fileAction(rel, "stage") }

// Unstage removes a path from the index (keeps working-tree changes).
func (s *Service) Unstage(rel string) (Status, error) { return s.fileAction(rel, "unstage") }

// StageAll stages every change (tracked + untracked).
func (s *Service) StageAll() (Status, error) {
	if s.root() == "" {
		return Status{Message: "Open a folder first."}, nil
	}
	if _, err := s.git(mutationTimeout, "add", "-A"); err != nil {
		return Status{}, err
	}
	return s.Status()
}

func (s *Service) fileAction(rel, action string) (Status, error) {
	if s.root() == "" {
		return Status{Message: "Open a folder first."}, nil
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		return Status{}, err
	}
	switch action {
	case "stage":
		_, err = s.git(mutationTimeout, "add", "--", clean)
	case "unstage":
		_, err = s.git(mutationTimeout, "restore", "--staged", "--", clean)
	default:
		return Status{}, fmt.Errorf("unsupported action %q", action)
	}
	if err != nil {
		return Status{}, err
	}
	return s.Status()
}

// Commit commits the staged changes with the given subject/body.
func (s *Service) Commit(subject, body string) (CommitResult, error) {
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if s.root() == "" {
		return CommitResult{Message: "Open a folder first."}, nil
	}
	if subject == "" {
		return CommitResult{Message: "A commit message is required."}, nil
	}
	stat := strings.TrimSpace(s.gitOK(diffTimeout, "diff", "--cached", "--stat"))
	if stat == "" {
		return CommitResult{Message: "No staged changes to commit."}, nil
	}
	args := []string{"commit", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	if _, err := s.git(mutationTimeout, args...); err != nil {
		return CommitResult{}, err
	}
	hash := strings.TrimSpace(s.gitOK(statusTimeout, "rev-parse", "HEAD"))
	status, err := s.Status()
	if err != nil {
		return CommitResult{}, err
	}
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}
	return CommitResult{Hash: hash, ShortHash: short, Subject: subject, Message: "Committed.", Status: status}, nil
}

// --- git execution ---

func (s *Service) root() string {
	if s.roots == nil {
		return ""
	}
	return strings.TrimSpace(s.roots.Root())
}

func (s *Service) git(timeout time.Duration, args ...string) (string, error) {
	if err := rejectNetworkGit(args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.root()
	cmd.Env = nonInteractiveEnv(os.Environ())
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		// Wrap the sentinel so callers can distinguish a timeout from a normal
		// non-zero exit (e.g. "path absent at HEAD") via errors.Is.
		return "", fmt.Errorf("git %s timed out: %w", firstArg(args), context.DeadlineExceeded)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return string(out), nil
}

func (s *Service) gitOK(timeout time.Duration, args ...string) string {
	out, err := s.git(timeout, args...)
	if err != nil {
		return ""
	}
	return out
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// rejectNetworkGit blocks silent network operations; those need an explicit,
// user-driven flow. It locates the real subcommand even behind global flags
// (e.g. `git -c k=v -C dir fetch`), so the guard can't be sidestepped by
// prefixing flags before the verb.
func rejectNetworkGit(args []string) error {
	sub := firstSubcommand(args)
	switch sub {
	case "fetch", "pull", "push", "clone", "ls-remote":
		return fmt.Errorf("git %s requires an explicit network workflow", sub)
	}
	return nil
}

// firstSubcommand returns the first non-flag token, skipping git's global flags
// — including the ones that take a separate value argument (-c, -C, --git-dir,
// --work-tree, --namespace, --exec-path, --super-prefix).
func firstSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if a == "" {
			continue
		}
		if !strings.HasPrefix(a, "-") {
			return strings.ToLower(a)
		}
		switch a {
		case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix":
			i++ // this flag consumes the following arg as its value
		}
	}
	return ""
}

func nonInteractiveEnv(base []string) []string {
	overrides := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GCM_INTERACTIVE=Never",
		"GIT_OPTIONAL_LOCKS=0",
	}
	skip := map[string]struct{}{}
	for _, e := range overrides {
		k, _, _ := strings.Cut(e, "=")
		skip[strings.ToUpper(k)] = struct{}{}
	}
	env := make([]string, 0, len(base)+len(overrides))
	for _, e := range base {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if _, dup := skip[strings.ToUpper(k)]; dup {
			continue
		}
		env = append(env, e)
	}
	return append(env, overrides...)
}

func repoUnavailableMessage(err error) string {
	if err != nil {
		t := strings.ToLower(err.Error())
		if strings.Contains(t, "dubious ownership") || strings.Contains(t, "safe.directory") {
			return "Git blocked this repo for dubious ownership. Run: git config --global --add safe.directory <path>"
		}
	}
	return "This folder is not a Git repository."
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// --- porcelain parsing (ported, -z aware) ---

func parseStatusZ(output string) ([]FileChange, string) {
	records := strings.Split(output, "\x00")
	changes := []FileChange{}
	aheadBehind := ""
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if strings.TrimSpace(rec) == "" {
			continue
		}
		if strings.HasPrefix(rec, "## ") {
			aheadBehind = parseAheadBehind(rec)
			continue
		}
		if len(rec) < 4 {
			continue
		}
		index := strings.TrimSpace(rec[:1])
		worktree := strings.TrimSpace(rec[1:2])
		// The path runs from byte 3 to the NUL boundary. With -z +
		// core.quotepath=false it is literal, so it must NOT be trimmed —
		// filenames can legitimately contain leading/trailing spaces.
		path := rec[3:]
		oldPath := ""
		if isRenameOrCopy(index) && i+1 < len(records) {
			i++
			oldPath = records[i]
		}
		changes = append(changes, FileChange{
			Path:     path,
			OldPath:  oldPath,
			Index:    index,
			Worktree: worktree,
			Summary:  changeSummary(index, worktree, oldPath),
		})
	}
	return changes, aheadBehind
}

// isRenameOrCopy reports whether a porcelain v1 -z record is followed by a
// second NUL-separated original-path record. Only a rename/copy detected in the
// INDEX (the X status) emits that extra record; porcelain v1 does not produce a
// worktree-side R/C, so keying on the index status alone keeps the record stream
// in sync (keying on the worktree side would consume a record that isn't there).
func isRenameOrCopy(index string) bool {
	return index == "R" || index == "C"
}

func parseAheadBehind(line string) string {
	start := strings.Index(line, "[")
	end := strings.LastIndex(line, "]")
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(line[start+1 : end])
}

func splitChanges(changes []FileChange) (staged, unstaged []FileChange) {
	staged = []FileChange{}
	unstaged = []FileChange{}
	for _, c := range changes {
		if c.Index != "" && c.Index != "?" {
			s := c
			s.Staged = true
			staged = append(staged, s)
		}
		if c.Worktree != "" || c.Index == "?" {
			u := c
			u.Staged = false
			unstaged = append(unstaged, u)
		}
	}
	return staged, unstaged
}

func changeSummary(index, worktree, oldPath string) string {
	if oldPath != "" || index == "R" || worktree == "R" {
		return "renamed"
	}
	if index == "?" || worktree == "?" {
		return "untracked"
	}
	if index == "A" || worktree == "A" {
		return "added"
	}
	if index == "D" || worktree == "D" {
		return "deleted"
	}
	if index == "M" || worktree == "M" {
		return "modified"
	}
	return "changed"
}

func cleanRelPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	value = filepath.ToSlash(value)
	value = strings.TrimPrefix(value, "/")
	if value == "" || value == "." {
		return "", errors.New("a path is required")
	}
	if strings.HasPrefix(value, "-") {
		return "", errors.New("path cannot start with a dash")
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../") ||
		strings.Contains(value, "/../") || strings.HasSuffix(value, "/..") {
		return "", errors.New("path must stay inside the repository")
	}
	return value, nil
}
