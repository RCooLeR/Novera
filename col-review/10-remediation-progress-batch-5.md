# Remediation Progress — Batch 5 and Re-review

Date: **2026-07-18**

Reviewed baseline and current committed `HEAD`: **`46c90bba35d41a74d356f8ade7cb55999499c9c7`**

Local annotated safety tag: **`pre-remediation-2026-07-16`**, dereferencing to the same baseline commit

Status: **fifth uncommitted implementation batch plus a complete finding-by-finding reconciliation; public release remains frozen**

## Scope and conclusion

This document continues [09-remediation-progress-batch-4.md](09-remediation-progress-batch-4.md). The original 142 records remain the historical review of the tagged source. This batch re-read every review ledger against the current working tree, implemented another set of independently fixable items, ran adversarial follow-up checks, and retained partial/open status wherever the full acceptance criterion is not yet met.

The review is **not closed**. The current conservative reconciliation is:

| Current status | Architecture | Backend | Frontend | Delivery/quality | Total |
| --- | ---: | ---: | ---: | ---: | ---: |
| Remediated in the cited/current surface | 2 | 34 | 30 | 18 | **84** |
| Partial or contained | 12 | 6 | 9 | 16 | **43** |
| Open | 2 | 2 | 6 | 5 | **15** |
| Total finding records | 16 | 42 | 45 | 39 | **142** |

“Remediated in the cited/current surface” is narrower than “the project is production-ready.” A partial architecture or release-gate record can remain even when its concrete bug has been fixed. All changes remain unstaged and uncommitted.

## Backend, authorization, and data-integrity changes

| Findings | Status | Implementation and adversarial proof | Important residual work |
| --- | --- | --- | --- |
| BE-004, AR-002 | **Substantially narrowed; AR-002 remains partial** | Bound `Workspace.ReadRaw`/`WriteRaw` and `Watcher.Suppress` methods were removed. Internal adapters provide producer-only access, generated bindings were regenerated, and surface tests reject those methods. | The application still binds implementation services directly. Existing checks are focused contracts, not one exact allowlist covering every service and generated method. Purpose-built bridge facades remain the endpoint. |
| BE-006 | **Remediated for production Agent/LLM/HTTP clients** | One direct-only dial policy disables ambient proxies; resolves every hostname; rejects an entire mixed public/non-public answer set; permits exact localhost names only when all results are loopback; blocks private, metadata, link-local, multicast, documentation, benchmark, and other non-public ranges; dials a vetted numeric address; pins pooled connections; and repeats policy on each new connection. Resolver, literal-address, mixed-answer, redirect/pool, and client policy-contract tests pass. | Explicit authenticated proxy support is not implemented. OS resolver and real-network integration remain environment-dependent. |
| BE-011, FE-010, AR-004 follow-up | **Process-tree and terminal ownership materially remediated; application-wide executor remains partial** | Agent commands bound output during acquisition, retain diagnostic prefixes, return timeout/cancel/start/non-zero/ownership failures, and kill the owned tree with a Windows Job Object or Unix process group. Terminal sessions use the same ownership model, reject a missing/non-directory workspace, terminate descendants, and rebind across workspace generations with pending start/close protection. | DB, every Big File operation, transforms, watcher producers, and generic Jobs are not yet under one application-wide generation/cancel/await/terminal-state executor. Blocked OS reads and platform-specific descendant behavior still need native integration coverage. |
| BE-012, BE-013 | **Remediated in Agent rollback/undo** | Rollback snapshots are bounded before retention. Undo validates the complete plan first and fails before mutation when a target is unreadable, a directory, or exceeds budget; it cannot report success after partial undo. Normal/race tests cover oversize and partial-failure cases. | Rollback remains Agent-local rather than the durable authenticated recovery state machine required by BE-033/AR-005. |
| BE-014 | **Remediated for `Shell.SaveTextFile`; broader output policy remains partial** | The shell save path writes a unique same-directory temporary, syncs/closes it, publishes atomically, and preserves the prior destination on failure. | Multi-output atomicity, ACL/xattr/ownership policy, directory-handle publication, and a product-wide output transaction remain open. |
| BE-017, BE-018, BE-020 | **BE-017 and bounded legacy data tools remediated; BE-018 remains partial** | Dump transforms operate on lexer-recognized SQL code rather than strings/comments/backticks; literals and identifiers are validated; regex replacement semantics cannot escape into SQL; option transforms are DDL-only. Legacy extraction indexes multiple exact table spans, rejects schema ambiguity/truncation, and shares ownership with split. SQL acquisition has bounded prefixes, logical lines, and statements with cancellation and pre-publication checks. | Big File `plugins/sql/extract.PlanTableRanges` still models one contiguous table slice. All-DDL-then-data, repeated/interleaved blocks, routines, and dialect completeness remain open there. Tuple materialization is bounded by the statement cap rather than streamed field-by-field, and a blocked OS read cannot be force-cancelled portably. |
| BE-019 | **Remediated in dump-to-CSV** | Qualified requests match exact schema/table identity; bare ambiguity fails; truncated COPY/INSERT blocks fail; all COPY bodies, including non-target tables, stay opaque until `\.`; octal/hex escapes decode; unknown escapes retain their backslash. PostgreSQL `\N` remains a distinct CSV null marker, empty remains empty, and a literal `\N` is emitted escaped as `\\N`. Regressions cover non-target COPY bodies resembling SQL and null/empty/literal separation. | The null-marker convention must remain documented for downstream consumers; generic CSV has no intrinsic nullable type. |
| BE-034, FE-022, AR-015 | **Exact operation approval remediated; wider AR-015 remains partial** | Mutating Agent calls build deterministic indented canonical JSON containing version, run/call/tool identity, complete bounded arguments, workspace root/generation, bounded resource identities, and expiry. The UI paginates the exact hashed bytes and shows digest/expiry. Backend digest comparison is constant-time, one-use, expiring, and followed by workspace/resource revalidation. `create_artifact` is gated. Fanout is capped at 4,096 resources and aggregate content hashing at 64 MiB before prompting. Tests cover ordering, tail mutation, mismatch, replay, expiry, drift, oversize, and excessive artifact fanout. | AR-015 also covers broader secret-capability and ambient-authority architecture. Approval does not make an unsafe tool safe, and native assistive-technology E2E coverage is absent. |
| BE-039 | **Partially remediated** | CSV indexes use `encoding/csv.InputOffset`; browse retains a pending parsed row instead of dropping it at a byte boundary; row/page responses carry explicit capped reasons. Retained filter/sort state, cells, headers, and pages have immutable budgets. `QueryTable` fails closed instead of returning a non-advancing `HasMore` page when the first row cannot fit. | `encoding/csv` still materializes a complete logical record/cell before the post-read 1 MiB cell check. An enormous quoted record can allocate before rejection. Sort/filter has no disk-spill/external algorithm. |
| BE-040 | **Remediated for reviewed chunk decoding** | `FileChunk` carries encoding and aligned actual offsets. UTF-8, UTF-16LE/BE, Latin-1, and binary windows preserve decoder-safe boundaries; invalid UTF-8 fails rather than returning replacement-corrupted text; binary ranges remain byte-exact. | BOM-less UTF-16 remains heuristic, and external mutation between requests still requires content/generation refresh. |

## Frontend lifecycle, events, and accessibility

| Findings | Status | Implementation and adversarial proof | Important residual work |
| --- | --- | --- | --- |
| FE-003, FE-004, AR-006 | **Meaningfully contained; global coordinator remains partial** | Workspace open/close and menu Quit refuse while any editor or staged Big File resource is dirty. `beforeunload` is registered while unsaved resources exist. Big File session IDs/dirty state are shared with the store; tab close uses the accessible confirmation and requires successful backend discard before “Discard & Close.” Large-file panes remain mounted while other tabs are active, so tab switching no longer destroys staging. Delete/rename already fail closed around large-file resources. | There is no one Save All / Review / Discard / Cancel coordinator with resumable originating actions. Native OS-window close is not proven to honor WebView `beforeunload`. Active operations and every resource kind are not registered in one lifecycle service. |
| FE-005 | **Remediated** | Entering edit stops Follow; staged edits disable Follow and stop its effect. Backend `RefreshFile` uses atomic `ReopenIfClean` and returns `ErrStagedEdits`, so no renderer race can refresh away staged content. Registry/service tests prove generation and staging remain unchanged. | A future external-change coordinator should offer compare/rebase UX instead of only refusing refresh. |
| FE-008 | **Remediated in the single-window staging path** | Stage mutations use a per-view serial queue and read updated original length only at queue head. `stageNow` returns success/failure; Done/Save Patch/Save Copy stop on failure. Saving disables the textarea; discard drains pending stages. Queue tests prove strict ordering and continuation after rejection. | Broader Big File request/session generations and external-change conflicts remain FE-015. |
| FE-014, FE-016 | **Remediated in cited flows** | Table filter/sort/window requests use operation generations and discard stale responses. Late `BigFile.OpenFile` completion after unmount closes the newly created backend session. | Other Big File/BigTools requests still need consistent generations (FE-015). |
| FE-021 | **Remediated** | Switching provider no longer deletes a stored API key. Settings and Assistant expose an explicit dangerous removal behind accessible confirmation; deletion is awaited and authoritative credential status refreshes without changing provider/model. | Endpoint/provider changes still depend on backend credential-origin policy. |
| FE-031 through FE-037, FE-042 | **Remediated in source interactions; native/WebView verification remains** | Editor tabs implement tablist/tab/tabpanel, roving focus, arrows/Home/End/Delete, close controls, and Ctrl/Cmd+W. Dialogs share focus trap, Escape, initial focus, and focus restore. Search/Problems/Source Control/grids/tree/context menus and app/encoding menus gained keyboard navigation. Splitters expose separator values and keys. Status announcements, focus-visible, reduced motion, splash focus behavior, responsive sizing, and awaited clipboard feedback were added. | FE-032 remains partial because there is no inert-background/stacked-dialog manager. Real WebView screen-reader, high-contrast, zoom, keyboard-only, and minimum-window E2E tests remain FE-040/QR-012. |
| FE-039 | **Remediated for the current lockfile** | Vite/Vitest/React plugin were upgraded to non-vulnerable releases. `npm audit --audit-level=moderate` reports zero vulnerabilities and is enforced after lockfile-strict CI install. | Advisory state is time-dependent and needs ongoing Dependabot/CI maintenance. |
| FE-041 | **Remediated for reviewed bridge events and TypeScript seams** | LLM, Agent, filesystem, watcher-error, and menu payloads enter as `unknown` and pass strict type/required-field/sequence/plan/allowlist parsing. Unknown fields are discarded. Malformed stream/completion/error/Agent events visibly fail and cancel the active operation. `noImplicitAny` and ESLint `no-explicit-any` are enabled; parser tests cover malformed/non-coercible input. | The protocol still lacks generated versioned schemas and real malformed-event WebView integration. Durable top-level diagnostics remain AR-013. |

## CI, dependency, and supply-chain changes

| Findings | Status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| QR-009, QR-010 | **Reachable vulnerability fixed and gates expanded; QR-010 remains partial** | CI runs `govulncheck`; `go-git` and transitive modules were upgraded to eliminate reachable GO-2026-5496. Frontend audit, lint, typecheck, tests, module verification, production build, and platform checks are explicit. | Full hosted evidence, complete race/coverage/fuzz/staticcheck matrices, retained timeout diagnostics, and native integration remain incomplete. |
| QR-018, QR-019, QR-021, QR-022, QR-023 | **Remediated in source inputs** | Docker bases are digest-pinned; Zig/SDK downloads are SHA-verified; `GOTOOLCHAIN=local` rejects substitution; restrictive Docker ignore files reduce context; actions are pinned to full commits/current Node generations; Dependabot tracks Go, npm, Actions, and Docker. | Renovation PRs need review and hosted execution. Real Docker/Buildx/QEMU was unavailable locally. |
| QR-035 | **Remediated in workflow source; external enablement remains** | Security workflow source adds CodeQL, dependency review, Gitleaks, and `govulncheck`, with pinned actions. Workflow/action syntax and local scanners passed. | Repository security-events/GHAS permissions, branch protection, push protection, organization policy/licensing, and hosted results remain release gates. |

## Independent re-review defects fixed

The reconciliation found and fixed four additional concrete regressions:

1. Non-target PostgreSQL COPY data resembling `COPY target ...` could be parsed as SQL and emitted as false target data. Every COPY block is now opaque unless selected.
2. A table page whose header fit the response budget but whose first row did not could return `HasMore` without advancing. Browse and filtered paths now fail closed.
3. `create_artifact` intent construction could hash excessive fanout/content before prompting. Resource count and aggregate content-hash budgets now apply before capture.
4. COPY `\N` still collapsed into an empty CSV value. Null, empty, and literal marker values are now distinct and regression-tested.

## Additional residuals confirmed by the final manual audit

- Exact approval revalidation still has a narrow cross-process TOCTOU window before dispatch. Directory identity does not recursively bind contents, and derived `split_dump` output names are not all known when the approval intent is built.
- The bounded SQL terminator is not a complete dialect parser for every comment, dollar-quoted string, or same-line multi-statement construct. A same-line statement sequence can still make table-option transformation ownership ambiguous.
- Big File CSV physical-line window alignment is not record-aware around quoted multiline fields. The table cache key remains size plus mtime, so an external same-size change with a preserved timestamp can evade invalidation.
- On Windows, a command/terminal process is assigned to its Job Object immediately after start rather than created suspended inside it. A deliberately racing child has a small pre-assignment escape window.
- The dial policy is deliberately conservative but has not been exhaustively classified against every IPv6 transition/translation prefix. HTTP redirect policy still permits the reviewed same-origin `www` host toggle.

## Validation evidence

### Passed

- Safety tag dereference and `HEAD`: **`46c90bba35d41a74d356f8ade7cb55999499c9c7`**.
- Wails bindings: **385 packages / 13 services / 143 methods / 1 enum / 75 models**.
- Frontend Vitest 4.1.10: **20 files / 143 tests passed**.
- TypeScript passed with `strict` and explicit `noImplicitAny: true`.
- ESLint passed with `@typescript-eslint/no-explicit-any` as an error.
- `npm audit --audit-level=moderate`: **0 vulnerabilities**.
- Production Vite 8.1.5 build passed: **2,773 modules transformed**. CSP and lazy-entry checks passed. Monaco/editor workers remain dynamic and large.
- Focused normal/race suites passed during implementation for Agent approval/commands/network, Netsafe, LLM, Terminal, Workspace/table/output, Big File/session/CSV/SQL, Datatools, Jobs, and related changed packages. Focused vet passed in the backend workstreams.
- The scoped full Go suite passed after bounded SQL/extraction work and before the final small re-review fixes. Every later fix passed its affected package tests; the latest Datatools and Big File/session tests passed after final edits.
- Final serial `go vet`, `go mod verify`, `go mod tidy -diff`, `govulncheck`, and `git diff --check` passed. Actionlint/YAML and Gitleaks passed in the supply-chain workstream.

### Windows security-tooling limitation

A final uncached all-package rerun was attempted after integration. Windows security tooling again suspended newly linked `*.test.exe` processes at zero CPU, including Big File and several package binaries. Serial execution and workspace-local temporary/cache directories reproduced it. Exact orphaned validation processes were targeted for termination; Windows returned access denied for several already-suspended processes. No assertion failure was emitted, but the final all-package rerun cannot truthfully be claimed as passed.

This is an environment/driver limitation, not converted into a product failure or false green. Prior complete passes plus focused post-change normal/race tests provide current local evidence. A clean CI/hosted run with retained process diagnostics is required.

## Finding-by-finding reconciliation

- Architecture remediated in cited surfaces: **AR-014, AR-016**. Partial/contained: **AR-001–007, AR-009, AR-010, AR-012, AR-013, AR-015**. Open: **AR-008, AR-011**.
- Backend remediated in cited surfaces: **BE-001, BE-003–017, BE-019–022, BE-024–026, BE-028–031, BE-034–038, BE-040, BE-042**. Partial/contained: **BE-002, BE-018, BE-023, BE-027, BE-039, BE-041**. Open: **BE-032, BE-033**.
- Frontend remediated in cited surfaces: **FE-001–002, FE-005, FE-008, FE-010, FE-014, FE-016, FE-018–031, FE-033–037, FE-039, FE-041–042, FE-044**. Partial/contained: **FE-003–004, FE-006–007, FE-009, FE-012, FE-032, FE-038, FE-040**. Open: **FE-011, FE-013, FE-015, FE-017, FE-017A, FE-043**.
- Delivery/quality remediated in source/cited surfaces: **QR-005, QR-008–009, QR-015–019, QR-021–023, QR-027–028, QR-030–031, QR-035–036, QR-039**. Partial/contained: **QR-001–004, QR-006, QR-010–013, QR-025, QR-029, QR-033–034, QR-037–038, QR-040**. Open: **QR-014, QR-020, QR-024, QR-026, QR-032**.

## Highest-priority work still open

1. **Native-aware dirty-resource coordinator (FE-003/004, FE-011/013, AR-006):** intercept OS close and every destructive transition; enumerate all resource types; support Save All / Review / Discard / Cancel; resume only the exact originating action after settlement.
2. **Pre-commit workspace identity for every producer (BE-027, AR-003/004):** capture root/generation and revalidate immediately before table/artifact/output publication; merge producers into one executor.
3. **Big File multi-span SQL ownership (BE-018):** replace contiguous ranges with statement/block ownership across all-DDL-then-data, repeated, interleaved, routine, trigger, and dialect cases.
4. **Acquisition-time CSV limits (BE-039):** replace post-read `encoding/csv` limits with a bounded parser; add external sort/spill or explicit refusal budgets.
5. **Authenticated recovery/durable transactions (BE-002, BE-033, BE-041, AR-005):** identity-bound authenticated journals, preview/confirmation, crash fault injection, multi-output state machines, cross-process CAS/locking, and metadata semantics.
6. **External-change and request generations (FE-011, FE-013, FE-015):** compare/reload/save-copy conflicts and cancellation/generation for Big File/BigTools and outstanding producers.
7. **Exact bridge facades/diagnostics (AR-002, AR-013):** allowlists for all 13 services, versioned event schemas, rejected-promise/error boundaries, and durable diagnostics.
8. **Release evidence/governance:** clean hosted CI/security runs, native package/install/launch tests, platform version identity, checksums/SBOM/provenance, signing/notarization, project/upstream licensing, security/contribution policy, and release commitments.

## Release decision

The fifth batch closes many concrete security, data-loss, accessibility, dependency, and supply-chain defects, but it does **not** close the review and does **not** authorize public release. The largest remaining engineering risks are native dirty-resource lifecycle, cross-service operation ownership, Big File SQL/CSV acquisition semantics, authenticated recovery/cross-process commit, and exact bridge facades. Signing, hosted evidence, platform packaging, and governance remain external blockers.
