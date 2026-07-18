# Large-file engine

Novera is designed to inspect and transform large logs, SQL dumps, and delimited
files without loading the entire source into one application buffer. On the
large-file route, bounded, line-aligned read windows cross the Go↔JavaScript
bridge rather than whole-file content. Practical limits still depend on the
operation, record and line structure, filesystem, address space, available
memory, and free disk space; Novera does not promise support for every file size.

The engine was historically ported from a project named Quarry and lives under
`internal/bigfile/`. The reviewed tree does not identify a verifiable upstream
repository, revision, or license for that port. That provenance must be added
before public distribution; this document intentionally does not substitute a
generic or guessed link.

## How files are routed

When a file is opened from the tree (`store.openFile`):

- **Tabular** (`.csv`, `.tsv`, `.xlsx`, `.xlsm`) → the analytics grid
  (`TableView`), which streams a sliding row window and colours CSV/TSV columns
  with a rainbow palette.
- **Over 64 MiB or binary** → the big-file viewer (`BigFileView`), backed by the
  streaming engine.
- **Otherwise** → the Monaco editor.

## Viewer (`BigFileView`)

Windowed scrolling keeps only a ring of rows near the viewport in the DOM (the
engine normally serves ~1 MiB text windows / 64 KiB hex windows). The active
read/bridge window is bounded, but total work and storage are not constant:
background indexes, cached metadata, very long records, transform state, OS
cache, output files, and recovery data can grow with the source or operation.
Features:

- **True line numbers** — a sparse line index is built in the background; numbers
  are approximate (shown with `≈`) until it completes, then exact.
- **In-file search** — find next/previous with regex and match-case, encoding
  aware, time-bounded.
- **Go to** line number, `0x` byte offset, or percent.
- **Hex view** (automatic for binaries) and **follow-tail** for live logs.
- **Per-line syntax highlighting** for ~45 languages plus a dedicated SQL
  highlighter (`frontend/src/lib/lineHighlight.ts`), tokenised per line so a
  collapsed/truncated long line never bleeds colour.

## Editing

Editable for UTF-8 / ASCII files with LF line endings (byte-exact round-trip is
only guaranteed there). Edits are **staged** in a piece table in the engine
(`StageEdit`), never written to the source until you save:

- **Save in place** (`SavePatch`) — length-preserving edits only; applied with a
  reverse-patch recovery sidecar. If recovery evidence is incomplete or does
  not match the source, Novera fails closed and preserves it for explicit
  recovery instead of claiming automatic crash recovery succeeded.
- **Save as copy** (`SaveCopy`) — streams the edited file to a new path; the
  source is untouched.

Normal browsing, search, preview, and save-as-copy do not mutate the source.
`SavePatch` and explicitly confirmed replace/swap operations do mutate it; those
paths use precondition checks and recovery evidence, and report failures rather
than describing all source mutation as impossible.

## Data tools (`BigToolsModal`)

**Tools → Data Tools (big-file CSV/SQL)…** opens the active CSV or SQL file in
the engine and offers streaming transforms that each write a *new* file via a
native save dialog:

- **CSV** — filter, dedupe, sample, keep-columns, redact/anonymise, CSV→SQL,
  export JSONL/SQLite/XLSX, column profile.
- **SQL dumps** — lint, extract table (whole / schema-only / data-only), split by
  table, reshape INSERTs, sample fixture, find & replace, cleanup presets.
- **Any file** — regex harvest (one match per line).

"Streaming" describes source acquisition, not a universal constant-memory
guarantee. For example, exact deduplication retains keys for distinct records,
some formats/libraries maintain additional in-memory state, and transforms need
space for outputs and temporary/recovery files.

## State location and legacy migration

New large-file settings, diagnostic logs, and central line-index caches live in
the OS user configuration directory under `Novera/bigfile`. Set
`NOVERA_BIGFILE_HOME` only when an explicit portable/test location is required.
That override is the sole destination for new state. To keep an explicit
location isolated, setting it disables implicit `~/.quarry` discovery; set
`QUARRY_HOME` as well when a specific legacy directory should be imported.

Older builds used `QUARRY_HOME` or `~/.quarry`. If canonical Novera state is
missing, the engine treats those locations as read-only migration input:

- parseable settings are sanitized and atomically copied forward once;
- at most the bounded recent portion of the legacy log is imported;
- a legacy line-index cache is republished only when its cache version, source
  path/size/mtime, bounded sample hash, and completed flag match;
- an already-present canonical file wins, and Novera serializes migration with
  its own in-process settings/log writers; and
- malformed settings/caches are preserved and not imported, symlinked or other
  non-regular settings/log inputs are rejected, and malformed log lines are
  skipped when diagnostics are read.

Recovery/output suffixes such as `.quarry.*` intentionally remain compatible.
They describe an on-disk recovery protocol, not the current product identity,
and renaming them without a dual-reader migration could make recovery evidence
unreadable.

## Architecture (`internal/bigfile/`)

| Package | Role |
| --- | --- |
| `document` + `lineindex` | Bounded file reads, chunk cache, sparse line index |
| `session` | Open-file registry handing opaque ids to the frontend |
| `manualedit` | Piece-table edit staging (undo/redo, bounded insert buffer) |
| `inplace` | Recovery-journal-backed length-preserving in-place patching |
| `encodingx` | Encoding detection + codecs (UTF-8/16, Windows-125x) |
| `search` / `replace` | Streaming, time-bounded search and replace |
| `plugins/csv`, `plugins/sql/*` | CSV dialect/schema/profile, SQL analyse/extract/reshape/preset/diff |

`fileservice*.go` is the Wails-bound surface; `main.go` registers it via
`bigfile.NewFileService()`. The bound methods serve windows, search, edit
staging, saves, hex, and CSV/SQL transforms. Bound request/response DTOs are
size-limited; output-producing operations return metadata rather than output
file contents.
