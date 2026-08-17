# Quarry-to-Novera Large-File Tool Synchronization

Date: **2026-07-23**

Last updated: **2026-07-24**

Novera baseline: **`2a3ae0e229f618f355c5b9922589925bb8e11f42`**, branch **`codex/quarry-tool-sync`**

Quarry reference: branch **`codex/wave0-remediation`**, committed `HEAD`
**`318bb28a0480958e1da40a0b7aaf725f79e609a6`**, plus the local dirty working
snapshot present on 2026-07-23

Quarry snapshot state at comparison time: **478 changed/untracked/deleted paths**;
the tracked diff alone reported **215 files, 31,615 insertions, and 10,971
deletions**.

Status: **working-tree synchronization and focused re-review; public release
remains frozen**

## Scope and evidence boundary

Several Novera large-file packages were originally cloned from Quarry and then
diverged. This pass compared the current Novera implementation with Quarry's
current local implementation across:

- open-file identity, lifecycle, and retained-session ownership;
- bounded document windows, line resolution, tailing, and hex windows;
- search request ownership, exact match windows, cancellation, and stale-source
  rejection;
- staged editing, piece-table coordinates, save-copy approval, and shutdown;
- CSV parsing, encoded grid windows, schema/profile cursors, transformations,
  deduplication, anonymisation, JSONL, and CSV-to-SQL;
- SQL analysis, statement regions, extraction, splitting, reshaping, fixtures,
  schema diff, and serializer-aware replacement;
- plugin descriptor validation and the generated/frontend bridge contract;
- atomic publication and source revalidation shared by output-producing tools.

The Quarry reference is **not reproducible from its commit hash alone** because
the relevant fixes are present in a large dirty working tree. This review records
the branch, committed `HEAD`, dirty-path count, and tracked diff size so the
provenance limitation is explicit. Quarry was treated as a read-only reference;
all implementation changes were made only in Novera.

This comparison establishes technical lineage inside the two local workspaces.
It does **not** establish public-source provenance, redistribution permission, a
project licence, or third-party ownership. Those remain release/governance
decisions.

## High-level result

The original blanket SQL-dump replacement disablement was incorrect. Novera now
supports a deliberately bounded plain replacement mode for UTF-8 SQL dumps,
including `.dump` files, while preserving the safety property that malformed or
ambiguous structured data never produces a final output.

The larger synchronization also ports the high-value correctness work that made
Quarry's large-file tools generation-bound, source-verified, parser-bounded,
job-owned, cancellation-aware, and fail-closed. Features whose output semantics
are not yet strong enough remain visibly disabled instead of being advertised as
working.

The primary decoded viewer path is now routed through the same bounded document
window machinery. Text windows have a strict 1 MiB raw-source ceiling, decode
UTF-8, UTF-16LE/BE, and Windows-1251 on complete source-character boundaries,
and provide exact forward, previous, and tail continuation offsets. Focused
regressions include blank LF/CRLF rows, UTF-16 blank rows, BOM-only sources, and
the mixed case where 1,999 dense rows precede a sliced long line.

## User-visible SQL replacement correction

### Previous behaviour

The Big Tools panel displayed:

> Find and replace unavailable (serialization safety)

for every SQL dump. That converted a real safety concern into a blanket feature
removal and made ordinary WordPress dump migration impossible.

### Current behaviour

Plain find/replace is enabled when all of the following are true:

- the open source is detected as SQL, including the `.dump` extension;
- the source is non-binary UTF-8/ASCII;
- regex mode is off;
- the requested strings fit the explicit request budgets;
- the SQL stream remains within the supported lexical subset;
- every encountered PHP serialization payload is complete and structurally
  supported.

Replacement is limited to decoded **single-quoted SQL values**. The
implementation:

- does not replace inside comments, identifiers, statement syntax, or untouched
  literals;
- decodes and re-encodes supported SQL string escapes;
- parses native PHP/WordPress serialized values recursively;
- recalculates every affected `s:<byte-length>:"..."` field using UTF-8 byte
  length rather than character count;
- preserves serialized arrays, objects, references, integers, booleans, nulls,
  doubles, and nested values within explicit depth/value/size limits;
- writes a new file through an atomic no-clobber output transaction;
- revalidates the exact retained source before publication;
- removes the temporary output and publishes nothing on cancellation, source
  change, malformed serialization, ambiguous escape mode, unsupported routine
  or custom-delimiter syntax, short write, or commit failure.

Regex replacement remains disabled because arbitrary regex rewriting cannot
reliably identify the serialized string whose byte count must change. This is a
specific limitation, not a blanket serialization-safety disablement.

Focused regressions cover a `wordpress.dump` path and verify that replacing a
WordPress URL changes the serialized byte length correctly while leaving SQL
comments, identifiers, and unrelated values unchanged.

## Synchronization matrix

| Area | Quarry fixes/updates reviewed | Novera synchronization result |
| --- | --- | --- |
| File identity | Retained handle identity separated from display pathname; regular-file checks; exact source expectations | Ported. Long operations bind to a retained session and exact source fingerprint instead of trusting path/size/time alone. Stable rename/recreate is detected as an identity change; external-modification inspection revalidates the reopened handle against the current pathname after metadata sampling and rejects a path exchange during that read. |
| Session registry | Bounded sessions, leases, generation, close/refresh barriers, shutdown ownership | Ported with JS-safe monotonic generations, two-phase lifecycle barriers, and explicit shutdown draining. |
| Document windows | Newline-aware bounded windows, line resolution states, giant-line limits, external-change detection | Ported. Primary text windows reject negative or over-1-MiB budgets, decode UTF-8/UTF-16LE/BE/Windows-1251 without splitting a source character, and bracket rendering with retained-handle/path validation. Bounded reads validate before and after I/O and clear the chunk cache on detected source change. Resolution reports exact/pending/absent/limited instead of inventing a line number. |
| Line-index cache | Sparse-anchor persistence and stale-cache rejection | Ported with full-source SHA-256 matching. Full-digest computation is synchronized; large-source cache hydration is deferred to `StartIndexing`, and the retained source is revalidated before an owned snapshot is restored. A same-size middle rewrite with restored mtime cannot reuse cached anchors. |
| Search | Server-owned request registry, strict request IDs, quotas, cancellation, generation capture, match windows | Ported. Requests are bounded globally/per file, stale results are rejected, and UTF-16 match windows are decoded exactly. |
| Tail and reverse paging | One strict bounded range ending at the requested anchor or current EOF | Ported. Previous windows are required to end exactly at the current start, tail windows end exactly at the retained generation's EOF, and the frontend rejects a non-adjacent previous response. Blank/BOM-only sources and dense-row-plus-long-line windows have dedicated regressions. |
| Hex | File identity in response and bounded byte windows | Ported with a hard 64 KiB response cap and negative/overflow rejection. |
| Manual edit | Explicit source preparation, bounded piece table, revisioned approval, source-coordinate diff windows | Ported. Preparing an edit is a cancellable verification job; ordinary `StageEdit` never starts an implicit whole-file scan. |
| Save copy | Dialog-only destination authority, exact session/revision approval, atomic publication | Ported. The arbitrary-path `SaveCopy(fileID, dst)` bridge surface is removed; only native dialog selection is exposed. |
| Atomic output | No-clobber temp output, validation callback, cancellation/publication boundary | Ported. A cancellation or close cannot race a successful final commit into being reported as cancelled, and post-commit errors remain distinguishable. |
| CSV acquisition | Strict logical-record reader, field/record budgets, quote validation | Ported. Oversized or malformed records fail during acquisition instead of after unbounded `encoding/csv` materialisation. |
| CSV grids | Parser-confirmed record cursors and encoded windows | Backend and bridge contract ported for UTF-8, UTF-16LE/BE, and Windows-1251. Cursors authenticate file, generation, delimiter, and logical-record position. An optional interactive `GetCsvGrid` UI is not currently wired into Big Tools. |
| CSV transforms | Generation-bound requests, preflight validation, exact source verification | Ported. Missing/stale generations fail before the dialog and are checked again before publication. |
| CSV add column | Streaming/ragged-row preservation | Ported. Rows wider than the inferred header are not truncated. |
| CSV dedupe | Bounded key retention, exact collision comparison | Ported with 250,000-key/128 MiB defaults and 1,000,000-key/256 MiB hard caps; no unbounded fallback is used. |
| CSV anonymisation | Unpredictable per-export keyed pseudonyms | Ported using a fresh 32-byte HMAC-SHA256 key and a 128-bit encoded token; mode/key validation is strict. |
| CSV-to-SQL | Strict configuration, portable literals, identifier bounds, statement budgets | Ported. Records/fields, mapping, batch size, identifier suffixing, booleans, MySQL binary literals, and 64 MiB/10,000-row statement caps are validated. Big Tools exposes the simple conversion path; the optional full configuration/preview UI remains unwired. |
| SQLite/XLSX | Incomplete publication and format guarantees | Kept disabled. Backend calls fail before a save dialog and the frontend does not present controls. |
| Plugin descriptors | Deep cloning, cross-kind ID validation, panic/typed-nil containment, safety truth | Ported. A plugin-wide huge-file-safe claim can no longer override an unsafe operation. |
| SQL analyzer | Chunk-independent lexical state, qualified table identities, exact statement regions, limits | Ported. Analysis uses verified reads, fails closed on unsafe syntax, and publishes a bounded LRU cache only after final source validation. |
| SQL extraction | Non-contiguous exact regions and unrelated-DML exclusion | Ported. Whole/schema/data extraction and split use analyzed `Region` spans rather than contiguous table approximations. |
| SQL reshape | Conservative INSERT grammar, unsafe-client/routine guard, bounded statements | Ported, including partial-keyword overflow classification, conservative `--` comments, UTF-8 identifiers, and WordPress serialized tuple byte preservation. |
| SQL fixture sampling | Strict tuple grammar and unsafe-remainder scan | Ported. Reaching the requested row count does not hide unsafe later material in the bounded input. |
| SQL schema diff | Full bounded DDL lexer/parser and semantic dimensions | Backend and bridge contract ported in this synchronization pass; constraints, indexes, table options, column order, qualified identity, unknown status, cancellation, and aggregate budgets are retained. The optional two-source schema-diff UI is not currently exposed. |
| SQL cleanup presets | Transform semantics not proven for structured/serialized dumps | Kept disabled and omitted from the UI. |
| SQL regex replacement | Cannot safely maintain nested serialized byte lengths | Kept disabled with an explicit plain-replacement recommendation. |
| SQL plain replacement | Serializer-aware SQL value rewriting | Ported and enabled for supported UTF-8 SQL/dump inputs. |
| Bridge contract | Exact exported-method allowlist | Updated for intentional search/edit/lifecycle additions and removal of arbitrary-path save. Unexpected RPC additions still fail the contract test. |

## Safety invariants established or strengthened

### No implicit source mutation

Large-file edit, CSV, and SQL operations write a new artifact. Source swap,
automatic recovery replay, in-place patching, and arbitrary renderer-chosen
destinations remain unavailable. Compatibility RPCs that cannot be removed
without a binding migration reject before session or filesystem work.

### Exact source binding

Size and modification time are useful change hints but are not treated as an
artifact-integrity proof. Output operations use a full SHA-256 expectation plus
a bounded block map and verify consumed blocks before releasing bytes to the
transform. The complete expectation is checked again before final publication.

The present implementation caps the block map at 262,144 blocks and therefore
the supported exact-fingerprint envelope at 4 TiB. Fingerprint capture and final
validation remain O(file size); that cost is explicit and job-owned.

### Primary-window and document-read integrity

`GetWindow`, `GetNextWindow`, `GetPrevWindow`, and `GetTailWindow` use a strict
1 MiB maximum raw-source budget; zero selects that default, while negative and
larger values fail. Decoded windows use encoding-aware newline and
source-character boundaries for UTF-8, UTF-16LE/BE, and Windows-1251. A
previous window must end exactly at the supplied current start, and a non-empty
tail window must make raw progress to the retained generation's EOF. The
visual-row budget accounts for long-line slices separately from the 2,000
logical-row response cap, so dense rows followed by a long line remain adjacent
instead of being silently skipped.

The primary-window wrapper validates the retained document before and after
rendering. Each bounded document range read independently validates the retained
descriptor and current pathname before and after I/O; a detected mismatch also
clears cached chunks before returning `ErrSourceChanged`. External-modification
inspection opens the current regular file, samples it, and then revalidates the
opened descriptor, current pathname identity, size, and modification time before
returning replacement metadata.

Persistent line-index anchors require a full-source SHA-256 match in addition to
path, size, modification time, stride, completion, and snapshot validation. The
full-digest computation is mutex-serialized. Sources above the synchronous-open
threshold defer cache hydration to `StartIndexing` rather than blocking
`OpenFile`; hydration revalidates the retained source before restoring the owned
snapshot.

### Generation and request ownership

File generations are JS-safe monotonic integers. CSV cursors, schema/profile
results, search requests, edit preparation, SQL analysis cache entries, native
save-dialog approvals, and post-dialog reacquisition all bind to the intended
generation. Late results cannot silently install into a refreshed, closed, or
reopened session.

### Bounded memory and output

The synchronized paths have independent request, field, record, statement,
token, nesting, retained-key, retained-schema, response, and output budgets.
Budgets are enforced before or while acquiring input, not only after a parser has
already materialised it. Unsupported scale fails with no published output.

### Cancellation and publication

Long tool actions are owned by backend jobs. Cancellation is checked during
fingerprinting, parsing, scanning, transformation, flushing, validation, and
before commit. Final atomic publication is serialized with cancellation/close/
shutdown so the UI cannot report a successfully committed artifact as a
cancelled operation.

## Intentional divergences and disabled features

The goal was not byte-for-byte Quarry replication. Novera retains its own
application/service architecture and keeps the following conservative
differences:

- SQLite and XLSX large-file exports remain disabled.
- SQL structural-cleanup presets remain disabled.
- Regex SQL replacement remains disabled; plain serializer-aware replacement
  is enabled.
- Automatic recovery replay and source swap remain disabled.
- Large-file editing is copy-only; no ordinary UI action mutates the source.
- CSV dedupe has no disk-spill mode. It refuses work when its explicit retained
  key/byte budget is exhausted.
- Parser-authenticated CSV grid paging, the full CSV-to-SQL configuration
  surface, and two-source SQL schema diff are backend/bridge capabilities but do
  not yet have dedicated Big Tools controls. They remain optional product work,
  not claims about currently exposed UI.
- Multi-file SQL split publication is atomic per output, not all-or-none across
  the complete destination set.
- The legacy Wails alpha.79 runtime is unchanged in this pass. A framework
  upgrade requires separate native/binding evidence and was not folded into
  parser/tool synchronization.
- Novera uses its existing output transaction and verified-reader adapters
  rather than copying Quarry's platform-specific file/source packages
  verbatim.

## Residual gaps and recommendations

### High priority

1. **Platform-relative atomic authority remains incomplete.** The current
   output primitive is path-bound. A hostile concurrent parent-directory or
   temporary-path substitution is not fully excluded on every platform.
   Implement descriptor/handle-relative create, validate, fsync, and rename
   with cross-process locking and adversarial platform tests.
2. **One application-wide executor is still absent.** Big File has strong local
   job ownership, but editor, database, terminal, Agent, watcher, workspace, and
   artifact operations do not yet share one authoritative lifecycle/executor.
3. **Durable authenticated recovery is still absent.** Recovery is correctly
   inspection-only, but a future writable recovery design needs authenticated
   source/backup identity, crash reconciliation, cross-process exclusion, and
   user preview/approval.
4. **SQL split is not transactionally grouped.** A later table-output failure
   can leave earlier outputs committed. Add an operation-owned staging
   directory and group manifest, or document/retain the current partial-set
   semantics with resumable cleanup.

### Scale and performance

5. Add an external spill/sort strategy for exact CSV dedupe if workloads beyond
   the in-memory cap are required. Do not silently increase the cap.
6. Exact source fingerprint capture and final validation scan the whole file.
   Keep them off the UI thread, expose honest progress, benchmark multi-terabyte
   sources, and consider an OS-backed immutable snapshot/handle strategy where
   available.
7. Viewer/search `ValidateUnchanged` has no OS mutation-generation token and
   still uses retained identity, size, and modification time. A same-size
   in-place rewrite whose timestamp is restored can therefore evade ordinary
   window/polling/search stale detection. Full-digest line-index cache hydration
   rejects such a rewrite before restoring sparse anchors, and artifact
   publication remains protected by the stronger full expectation, but
   navigation/search should gain a platform mutation token or verified-block
   handshake. Treat native change notifications only as hints, never as the
   publication proof.

### Product/release evidence

8. Run the complete normal/race/fuzz/security/backend/frontend matrices on
   hosted clean Windows, macOS, and Linux runners. This Windows host can compile
   test binaries that Microsoft security tooling intermittently refuses to
   launch; a frozen local process is not a passing test.
9. Add native install/launch/quit, save-dialog, cancellation, keyboard,
   high-contrast, zoom, minimum-window, and screen-reader evidence.
10. Select and publish the project/contribution licence, verify the legal
    provenance of copied/local code, enable private vulnerability reporting,
    and complete signing/notarisation/SBOM/attestation verification before
    release.
11. Keep the release freeze. These tool fixes materially improve data safety but
    do not close the 51 partial architectural/delivery records from Batch 6.
12. Rerun the official Wails binding generator from a clean environment. It
    froze on this Windows host, so the intentional generated TypeScript changes
    were updated manually and protected by the exact method allowlist plus a
    test that recomputes every big-file `ByID` value with Wails' FNV-1a
    algorithm. That is strong local verification, but it is not a substitute
    for a clean official regeneration/diff gate.

## Validation record

Validation completed on 2026-07-24 EEST against the uncommitted
`codex/quarry-tool-sync` working tree based on
`2a3ae0e229f618f355c5b9922589925bb8e11f42`.

- `go vet -p=1 . ./internal/...`, `go mod verify`, `go mod tidy -diff`, and
  `git diff --check` all passed.
- Affected Go suites were compiled with
  `go test -c -gcflags=all=-l` and invoked directly with a 45-second test
  timeout. The root bridge-contract package, `internal/bigfile`,
  `replace`, `document`, `lineindex`, `plugins/csv`, `plugins/sql/analyze`,
  `plugins/sql/extract`, `plugins/sql/reshape`, `plugins/sql/schemadiff`,
  `plugins`, `search`, `session`, `manualedit`, `fileio`, `exportx`,
  `encodingx`, `regexutil`, `plugins/filetype`, and `asciifold` all passed.
  The document and line-index race runs also passed.
- The compile/direct-run shape was necessary because Windows application
  security on this host intermittently held ordinary newly emitted test or
  helper executables without producing test output. This is strong local
  evidence, but it is not a substitute for the hosted normal/race/fuzz matrix.
- `npm test` passed 32/32 files and 204/204 tests. `npm run typecheck`,
  `npm run lint`, and `npm run build` passed; the production CSP and entry
  lazy-load checks passed. Vite still reports the documented advisory that
  several editor/worker chunks exceed 500 KiB.

The Windows binary was rebuilt from the validated production frontend with
Go 1.26.5, the `production` tag, Wails `v3.0.0-alpha.79`, and embedded build
identity `0.1.0-dev`, `2a3ae0e-dirty`,
`2026-07-23T22:04:37Z`, channel `development`.

- artifact: `bin/Novera.exe`
- size: 38,356,480 bytes
- modification time: `2026-07-23T22:04:38Z`
- SHA-256:
  `981944B51C2F2EBC11E8CC3F80BD0D62A8F7484624D974182BAF227B15350FEA`
- PE inspection confirms icon, version, and application-manifest resources.
  Direct Win32 version-resource queries return product `Novera`, product
  version `0.1.0`, company `rcooler`, and description
  `Local-first AI workbench`.

Local validation is evidence for this working tree only. Hosted multi-platform,
native UX, signing, installer, provenance, and release evidence remain required
as listed above.

## Reconciliation with the original review

This document does not recalculate the original 142 finding records or rewrite
the historical Batch 1–6 evidence. The original count remains a review of its
recorded revision. This pass is a focused implementation/re-review record for
the later Quarry-derived large-file surface and should be read together with
[11-remediation-progress-batch-6.md](11-remediation-progress-batch-6.md).
