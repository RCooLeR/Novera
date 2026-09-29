# Large-file engine

Novera is designed to inspect and transform large logs, SQL dumps, and delimited
files without loading the entire source into one application buffer. On the
large-file route, bounded, line-aligned read windows cross the Go↔JavaScript
bridge rather than whole-file content. Practical limits still depend on the
operation, record and line structure, filesystem, address space, available
memory, and free disk space; Novera does not promise support for every file size.

The engine was historically ported from a project named Quarry and lives under
[src/internal/bigfile/](../src/internal/bigfile/). The reviewed tree does not
identify a verifiable upstream repository, revision, or license for that port.
That provenance must be added
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
engine serves text windows with a strict 1 MiB raw-source maximum and 64 KiB hex
windows). The active read/bridge window is bounded, but total work and storage
are not constant:
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
  highlighter ([lineHighlight.ts](../src/frontend/src/lib/lineHighlight.ts)),
  tokenised per line so a collapsed/truncated long line never bleeds colour.

### Search semantics and remaining limit

Forward bounded regex search preserves whole-buffer non-overlapping match
alignment across chunks. Whole-word plain search preserves multibyte delimiter
boundaries, and dense searches observe cancellation between matches.

Backward regex search still evaluates non-overlapping matches within each read
window. Repeated overlapping candidates can therefore produce different
offsets when chunk alignment changes; reverse results are not guaranteed to be
the exact reverse of a complete forward scan. The
[2026-09-20 analysis](project-analysis-2026-09-20.md#remaining-findings-and-prioritized-follow-up)
contains a reproduction and the required semantic/performance follow-up.

### Text-window contract

The primary text-window RPCs use one encoding-aware document path:

- `GetWindow` and `GetNextWindow` decode a bounded range; `GetPrevWindow` must
  end exactly at the current window start, and `GetTailWindow` must end exactly
  at the retained generation's EOF.
- A zero byte budget selects the 1 MiB default. Negative budgets and requests
  above 1 MiB are rejected rather than clamped.
- UTF-8, UTF-16LE/BE, and Windows-1251 windows are decoded on complete source
  character and newline boundaries. A budget too small for one complete source
  character fails instead of emitting a replacement character.
- The response remains capped at 2,000 logical rows. Long-line visual slices
  are budgeted separately so they cannot consume the logical-row allowance and
  leave a gap before a previous/tail anchor.
- The frontend verifies that a previous response is exactly adjacent before it
  can prepend rows.

Focused regressions cover alternating forward/backward navigation across the
supported encodings, mixed newline forms, one-byte UTF-16 budget rejection,
blank LF/CRLF and UTF-16 rows, UTF-8 BOM-only input, and 1,999 dense rows
followed by a sliced long line.

### Retained-source and index-cache checks

Primary window rendering validates the retained document before and after the
window is built. Bounded document reads also validate the retained descriptor
and current pathname identity, size, and modification time before and after I/O.
If either check detects a source change, the document clears its chunk cache and
returns `ErrSourceChanged` instead of returning a mixed cached/fresh window.

Persistent sparse line-index anchors are restored only when path, size,
modification time, stride, completion state, snapshot bounds, and a full-source
SHA-256 digest match. The full-digest computation is synchronized. For sources
above the synchronous-open threshold, digesting and cache hydration are deferred
to `StartIndexing` rather than blocking `OpenFile`; the retained source is
revalidated before the owned snapshot is installed. This rejects a same-size
middle rewrite with restored modification time for index-cache reuse.

External-modification inspection does more than compare path metadata. It opens
the current path as a regular file, reads the bounded metadata sample, and then
revalidates the reopened descriptor against the current pathname, including
identity, size, and modification time. Rename/recreate and path-exchange races
are handled distinctly: stable rename/recreate is reported as an identity
change, while a path exchange during the sample read fails instead of returning
metadata for the wrong file.

One limitation remains: ordinary viewer/search `ValidateUnchanged` checks do not
have a platform mutation-generation token. A same-size in-place rewrite whose
timestamp is deliberately restored can evade normal polling/window stale
detection. Full-digest index-cache hydration and output-publication source
expectations are stronger, but navigation/search still need a platform mutation
token or verified-block handshake to close this gap.

## Editing

Editable for UTF-8 / ASCII files with LF line endings (byte-exact round-trip is
only guaranteed there). Entering edit mode first runs `PrepareEditSession`, a
cancellable, progress-reporting job that fingerprints the complete retained
source once. At most four prepared edit sessions may be retained. Subsequent
`StageEdit` calls authenticate only the bounded source blocks they consume;
they do not hide another whole-file scan behind every keystroke.

Edits are **staged** in a bounded piece table. The editor exposes **Save as
copy** (`SaveCopyViaDialog`), which asks the native save dialog for the
destination and streams the edited file to a new atomic output while leaving
the source untouched. The dialog approval binds the exact file generation,
piece-table session, and edit revision; changes made while the dialog is open
invalidate that approval. Leaving edit mode calls `ReleaseCleanEditSession`.
Only a clean session can be released; dirty staged content is preserved until
the user saves or explicitly discards it.

Direct in-place replacement is not exposed in the product UI. The internal
`SavePatch` RPC remains only as a hard-disabled compatibility boundary and
rejects before looking up a file session. Normal browsing, search, preview, and
save-as-copy do not mutate the source.

## Data tools (`BigToolsModal`)

**Tools → Data Tools (big-file CSV/SQL)…** opens the active CSV or SQL file in
the engine and offers streaming transforms that each write a *new* file via a
native save dialog:

- **CSV** — filter, dedupe, sample, keep-columns, redact/anonymise, CSV→SQL,
  JSONL export, column profile. SQLite/XLSX controls are intentionally absent.
- **SQL dumps** — lint, extract table (whole / schema-only / data-only), split by
  table, reshape INSERTs, sample fixture, and serialization-aware plain
  find/replace. Regex replacement and cleanup presets are intentionally absent.
- **Supported CSV/SQL files** — regex harvest (one match per line).

The panel materializes bounded column, warning, profile, and table pages rather
than building unbounded DOM controls. SQL extraction is based on analyzed
CREATE/INSERT/REPLACE regions and may omit preamble, ALTER/DROP, triggers,
routines, and other DML; the generated copy must be reviewed before import.

"Streaming" describes source acquisition, not a universal constant-memory
guarantee. Exact deduplication uses a bounded in-memory key set: the default
ceiling is 250,000 distinct records and 128 MiB of retained key data, callers
cannot raise it above 1,000,000 records or 256 MiB, and the transform fails
without publishing an output if either limit is reached. There is currently no
disk-spill mode. Other transforms can retain record- or batch-sized state and
all output operations need free space for an operation-owned temporary file
plus the final artifact.

CSV inspect, schema, preview, profile, and grid responses carry the retained
file generation. Grid cursors are authenticated to the file, generation,
delimiter, and logical-record position; transforms reject a missing or stale
generation before opening a save dialog and revalidate it before publication.
The grid can decode UTF-8, UTF-16LE/BE, and Windows-1251 windows. Artifact
transforms currently require UTF-8/ASCII input and fail before output creation
for unsupported source encodings instead of producing a corrupt copy.

The parser-authenticated `GetCsvGrid` RPC, full CSV-to-SQL configuration/preview
RPCs, and two-source SQL schema-diff RPC are implemented and bound, but dedicated
Big Tools controls for those optional surfaces are not currently exposed. The
visible CSV-to-SQL action uses the simpler conversion path.

Plain SQL find/replace operates only inside decoded single-quoted SQL values.
It recalculates native PHP/WordPress serialized byte lengths, preserves
comments, identifiers, and untouched literals, and writes a new copy. Regex,
custom-delimiter/routine contexts, ambiguous SQL escapes, malformed
serialization, and opaque serialized formats fail without publishing output.

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
unreadable. Recovery evidence is inspection/open-only in the product: the
frontend exposes no resume, apply, temporary-file deletion, or manifest deletion
controls.

The Big Tools export panel exposes JSONL only. SQLite and XLSX publication
remains fail-closed and those controls are intentionally absent until their
output paths have end-to-end publication guarantees.

## Architecture (`src/internal/bigfile/`)

| Package | Role |
| --- | --- |
| `document` + `lineindex` | Bounded file reads, chunk cache, sparse line index |
| `session` | Open-file registry handing opaque ids to the frontend |
| `manualedit` | Piece-table edit staging (undo/redo, bounded insert buffer) |
| `inplace` | Internal legacy recovery-journal reader; no recovery mutation controls are exposed in the UI |
| `encodingx` | Encoding detection + codecs (UTF-8/16, Windows-125x) |
| `search` / `replace` | Streaming, time-bounded search and replace |
| `plugins/csv`, `plugins/sql/*` | CSV dialect/schema/profile, SQL analyse/extract/reshape/schema diff; unsafe cleanup presets remain disabled |

`src/internal/bigfile/fileservice*.go` is the Wails-bound surface;
[src/main.go](../src/main.go) registers it via
`bigfile.NewFileService()`. The bound methods serve windows, search, edit
staging, saves, hex, and CSV/SQL transforms. Bound request/response DTOs are
size-limited; output-producing operations return metadata rather than output
file contents.
