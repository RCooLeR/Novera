# Large-file engine

Novera can open and edit files of **any size** — multi-gigabyte logs, SQL
dumps, CSVs — without loading them into memory. The whole file never crosses the
Go↔JS bridge or enters a single JS string; only bounded, line-aligned windows
do. The engine is a port of the [Quarry](https://github.com/) huge-file editor,
living under `internal/bigfile/` and exposed to the frontend as the bound
`FileService`.

## How files are routed

When a file is opened from the tree (`store.openFile`):

- **Tabular** (`.csv`, `.tsv`, `.xlsx`, `.xlsm`) → the analytics grid
  (`TableView`), which streams a sliding row window and colours CSV/TSV columns
  with a rainbow palette.
- **Over 64 MiB or binary** → the big-file viewer (`BigFileView`), backed by the
  streaming engine.
- **Otherwise** → the Monaco editor.

## Viewer (`BigFileView`)

Memory-bounded windowed scrolling: only a ring of rows near the viewport is in
the DOM (the engine serves ~1 MiB text windows / 64 KiB hex windows), so a
400 GB file costs the same as a 4 KB file. Features:

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
  crash-safe reverse-patch sidecar so a crash mid-save can be rolled back at next
  open.
- **Save as copy** (`SaveCopy`) — streams the edited file to a new path; the
  source is untouched.

The core invariant (from the upstream project) holds: **user source files are
never silently mutated.**

## Data tools (`BigToolsModal`)

**Tools → Data Tools (big-file CSV/SQL)…** opens the active CSV or SQL file in
the engine and offers streaming transforms that each write a *new* file via a
native save dialog:

- **CSV** — filter, dedupe, sample, keep-columns, redact/anonymise, CSV→SQL,
  export JSONL/SQLite/XLSX, column profile.
- **SQL dumps** — lint, extract table (whole / schema-only / data-only), split by
  table, reshape INSERTs, sample fixture, find & replace, cleanup presets.
- **Any file** — regex harvest (one match per line).

## Architecture (`internal/bigfile/`)

| Package | Role |
| --- | --- |
| `document` + `lineindex` | Bounded file reads, chunk cache, sparse line index |
| `session` | Open-file registry handing opaque ids to the frontend |
| `manualedit` | Piece-table edit staging (undo/redo, bounded insert buffer) |
| `inplace` | Crash-safe length-preserving in-place patching |
| `encodingx` | Encoding detection + codecs (UTF-8/16, Windows-125x) |
| `search` / `replace` | Streaming, time-bounded search and replace |
| `plugins/csv`, `plugins/sql/*` | CSV dialect/schema/profile, SQL analyse/extract/reshape/preset/diff |

`fileservice*.go` is the Wails-bound surface; `main.go` registers it via
`bigfile.NewFileService()`. The bound methods serve windows, search, edit
staging, saves, hex, and the CSV/SQL transforms — the whole-file content never
crosses the bridge.
