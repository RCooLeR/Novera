# Remediation Progress — Batch 6 and Residual Review

Date: **2026-07-18**

Reviewed baseline and current committed `HEAD`: **`46c90bba35d41a74d356f8ade7cb55999499c9c7`**

Local annotated safety tag: **`pre-remediation-2026-07-16`**, dereferencing to the same baseline commit

Status: **sixth uncommitted implementation batch plus another residual review; public release remains frozen**

## Scope and conclusion

This document continues [10-remediation-progress-batch-5.md](10-remediation-progress-batch-5.md). Batch 5 reconciled all 142 original finding records. Batch 6 re-audited every record that was still partial or open, implemented the bounded work that could be completed safely in the current local tree, and retained partial status wherever an acceptance criterion still depends on architecture consolidation, native/hosted evidence, owner policy, or a larger durable state machine.

The review is **not closed**. The conservative reconciliation is now:

| Current status | Architecture | Backend | Frontend | Delivery/quality | Total |
| --- | ---: | ---: | ---: | ---: | ---: |
| Remediated in the cited/current surface | 2 | 34 | 33 | 20 | **89** |
| Partial or contained | 12 | 8 | 12 | 19 | **51** |
| Open | 2 | 0 | 0 | 0 | **2** |
| Total finding records | 16 | 42 | 45 | 39 | **142** |

Only two original records remain wholly open, but this does **not** mean the product is release-ready. Several of the 51 partial records describe high-impact cross-cutting invariants: global resource lifecycle, one application-wide executor, authenticated recovery, cross-process commit semantics, bounded SQL/CSV acquisition, native evidence, signing, and governance.

All implementation changes remain unstaged and uncommitted.

## Native lifecycle and edit-conflict safety

| Findings | Current status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| FE-003, FE-004, AR-006 | **Further contained; remain partial** | The renderer still installs `beforeunload`, and now reports aggregate text/large-file dirty state to a native atomic gate. Wails `ShouldQuit` rejects application quit while dirty, and the main window's native `WindowClosing` hook cancels title-bar/window-manager close and emits a visible diagnostic. Bridge reports are serialized so older dirty/clean state cannot arrive after a newer state. A Shell state regression test was added, and production compilation covers the new host seam. | There is still no one Save All / Review / Discard / Cancel coordinator with resumable originating actions. Active operations and every resource type are not registered in one lifecycle service. Native interactive behavior still needs platform E2E coverage. |
| FE-013 | **Remediated in the cited conflict flow** | The dead-end “Keep mine” action was removed. A conflict retains the exact newly observed disk revision. Users can Save Copy, explicitly confirm Reload Disk, or explicitly confirm Overwrite Disk. Overwrite uses the observed external revision as a backend CAS token; another external change causes refusal. Reload captures the editor snapshot and refuses to apply if the user edits while the read is in flight. Tests cover revision capture, exact overwrite, absent-token refusal, newer-editor preservation, and reload/edit races. | A visual three-way compare/merge experience remains desirable. The current workflow preserves both versions and provides safe settlement, but does not automatically merge them. |
| BE-041/FE-013 follow-up | **Additional CAS gap remediated** | `Workspace.WriteFile` no longer interprets an empty expected revision as permission to replace an existing file. Empty means “the caller observed an absent path”; if another writer creates the path before commit, the write now returns `ErrStale` and preserves that file. Exact create and concurrent-create regressions were added. | Cross-process descriptor-based CAS/openat-style commit and metadata policy remain part of BE-041. |

## Frontend request ownership, stale data, and tools

| Findings | Current status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| FE-011 | **Materially improved; remains partial** | Table resources now join watcher subscriptions. Watcher changes bump a per-tab `sourceVersion`, which is included in TableView request identity and triggers refetch. Clean retained large-file sessions call `RefreshFile`; staged sessions stay stale, block in-place save, and require staging settlement before reload. Quick-open, search, diagnostics, and directory snapshots are invalidated on watched changes. | Unobserved directory changes still lack a canonical continuously updated workspace file index. Same-size/same-timestamp cache identity and richer compare/rebase UX remain backend/lifecycle work. |
| FE-012 | **Further contained; remains partial** | Watched resource changes and successful mutations invalidate or reconcile derived file/search/diagnostic indexes instead of indefinitely retaining stale paths. | Watch coverage is resource-oriented rather than a complete recursive canonical index. New paths in unwatched directories can still require explicit tree/index refresh. |
| FE-015 | **Remediated in the cited Big File view paths** | BigFileView now uses monotonic latest-request generations for open/reset, paging, find, goto, mode changes, follow-tail, and cleanup. Late results cannot commit after a newer request. Manual navigation stops Follow, and the former mode-switch timeout race was removed. | Backend operations that cannot portably interrupt a blocked OS read and application-wide operation ownership remain broader BE-032/AR-004 work. |
| FE-017, FE-017A | **Materially improved; remain partial** | Big Tools resets all target-derived state, reloads CSV schema on delimiter/header changes behind a generation gate, disables actions until session/schema readiness, and prevents modal close while work owns the session. Strictly parsed Big File job start/progress/end events drive records/note status, ID-matched reconciliation, and a Cancel action. | Jobs remain owned by the Big File service rather than one application executor. Native/WebView cancellation, close, and progress E2E evidence is still absent. |
| FE-043 | **Remediated in the cited shared-tool paths** | Every legacy shared tool action now acquires an `ExclusiveOperation` lease. Overlap is rejected with a diagnostic, and only the owning lease can clear `toolBusy`; an older completion cannot unlock a newer operation. Focused ownership tests pass. | A future unified executor should replace the renderer-local lease with backend-authoritative operation identity. |
| FE-040 | **Further evidenced; remains partial** | New generation, event-gate, operation-lease, watcher, conflict, and lifecycle tests expanded deterministic frontend coverage. The full suite now has 22 files and 152 tests. | There is still no DOM accessibility/native WebView E2E layer covering screen readers, zoom, high contrast, minimum window size, native dialogs, and OS close. |

## Backend jobs, workspace identity, and recovery

| Findings | Current status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| BE-027 | **Further contained; remains partial** | `QueryTable` and `TableInfo` capture workspace root plus generation. Browse/index/filter/sort cache keys bind generation and absolute source path; cache reads and publication revalidate the snapshot while holding the table lock. A delayed workspace-A index cannot install into workspace B. | Artifacts, every Big File producer, and remaining output paths still need the same pre-publication identity under one executor. |
| BE-032 | **Moved from open to partial** | A typed cancellable Big File job wrapper now preserves `context.Canceled`, rejects a concurrent job without losing the active handle, reports progress, and always clears owned job state. Reviewed SQL analyze/extract/split/schema/data/fixture/replace/reshape/preset operations, CSV project/add-column/SQL conversions, regex harvest, and edited-copy save use cancellable contexts. | This is still a private Big File manager, not the application-wide executor required by AR-004. Portable cancellation cannot forcibly interrupt every blocked OS read, and UI/native cancellation evidence remains incomplete. |
| BE-033 | **Moved from open to partial** | Replace manifests are staged to unique same-directory files, file-synced, atomically published, and directory-synced. All six pipelines and resume persist `output_written` immediately after promotion and `swapped` immediately after swap. Loads are capped at 1 MiB, strict about trailing data, reject unsafe persistence paths, and bind output/temp paths to the manifest filename. Cleanup re-loads trusted disk state. | Manifests still lack authenticated source/backup identity and a complete crash-reconciliation state machine. Cross-process locking/CAS, every swap fault point, user preview/confirmation, and metadata semantics remain open. |

## Bridge, performance, governance, and release evidence

| Findings | Current status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| AR-002, QR-024 | **Materially narrowed; remain partial** | `bridge_contract_test.go` records exact method allowlists for all 13 generated RPC service files, including the new native dirty-state report, and fails on a new binding or method. It also locks the existing Wails import boundary. `docs/wails-runtime-policy.md` defines alpha-update containment and stable-migration exit criteria. | Concrete implementation services are still bound directly rather than final purpose-built facades. Wails remains pre-stable and requires native upgrade evidence. |
| AR-013 | **Further contained; remains partial** | `docs/diagnostics-policy.md` defines bounded/redacted diagnostics, crash containment, user consent, and support-bundle requirements. Production builds now compile WebView developer tools off behind the `production` build tag; development builds retain them for local debugging. | Top-level renderer error boundaries, rejected-promise capture, rotating durable diagnostics, crash markers, and an implemented opt-in support bundle remain absent. |
| QR-014 | **Moved from open to partial** | Large-file benchmarks cover document open/window/giant-line behavior, line indexing, search, staging, and save paths. A scheduled/manual performance workflow records evidence and applies documented relative regression budgets. | Hosted baselines have not run; relative budgets do not replace product-level latency, throughput, allocation, and maximum-file acceptance targets on supported hardware. |
| QR-020 | **Moved from open to partial** | Release workflow source now generates CycloneDX SBOMs, requests GitHub artifact attestations, records `SOURCE_DATE_EPOCH`, and captures per-target build-environment evidence with pinned actions/tools. | Authenticode, Developer ID/notarization, Linux signing strategy, protected signing identities, independent verification, and hosted evidence remain release blockers. |
| QR-026 | **Remediated in repository source** | Added `SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, issue/PR templates, and a normative release checklist. README links the policies. | The owner must select a project/contribution license and enable/monitor GitHub private vulnerability reporting before release. Those are external decisions, not silently assumed here. |
| QR-032 | **Remediated** | The unreferenced, undocumented `all.cql` fixture was removed rather than shipped with unclear purpose/provenance. | Reintroducing a domain fixture requires documented origin, license, owner, tests, and an actual consumer. |
| QR-034 | **Further contained; remains partial** | CI now creates a tracked-source-only `git archive` and validates a clean-export build. The clean-checkout ordering bug was fixed by building `frontend/dist` before root Go compilation embeds it. | Hosted clean-export execution and native package/install/launch evidence are still required. |

## Independent residual-review defects fixed

The residual audit found four additional concrete defects or missing enforcement points:

1. **Production WebView developer tools were unconditionally enabled.** Build-tagged constants now keep them available only outside `production` builds.
2. **Native window close could bypass the renderer-only dirty guard.** Wails application quit and window-closing paths now consult native mirrored dirty state and reject close while unsaved resources exist.
3. **An empty optimistic revision could overwrite a concurrently created file.** Existing-path plus empty-revision writes now fail stale; empty remains valid only while the path is absent.
4. **A confirmed disk reload could still overwrite typing performed during its asynchronous read.** Reload now applies only if the exact editor snapshot is unchanged.

## Validation evidence

### Passed in the integrated Batch 6 tree

- Wails bindings regenerated from production-tagged source: **385 packages / 13 services / 144 methods / 1 enum / 75 models**.
- Frontend Vitest 4.1.10: **22 files / 152 tests passed**.
- TypeScript `--noEmit` and ESLint passed.
- Production Vite 8.1.5 build passed: **2,775 modules transformed**. CSP and lazy-entry assertions passed. Monaco/editor worker chunks remain large and dynamically split.
- `npm audit --audit-level=moderate`: **0 vulnerabilities**. The first retry encountered the host's Node CA-chain error; retrying with Node's system CA support reached the registry and returned the zero-advisory result.
- `go vet -p=1 . ./internal/...` passed.
- `go vet -tags production -p=1 .` passed, including the production DevTools build-tag path.
- `go mod verify` passed; `go mod tidy -diff` was clean.
- Backend focused Big File tests passed. Replace tests printed an `ok` package result; Windows then returned a process-cleanup/unlink failure for the completed test executable, so this is recorded as an environment cleanup failure rather than silently converted into a normal successful command.
- Actionlint, workflow/template YAML parsing, gofmt, and targeted diff checks passed in their workstreams. The exact bridge allowlist/import-boundary tests compiled under `go vet`; execution remains part of the blocked Go test matrix.

### Windows security/tooling limitation

The host still retains multiple zero-CPU Go test executables suspended by Windows security tooling. Per the prior evidence policy, Batch 6 did not launch another full Go test matrix. A final `govulncheck ./...` retry from the retained scanner binary was also suspended at zero CPU and was terminated after producing no result. Batch 5's scan found zero reachable vulnerabilities and dependency versions did not change in Batch 6, but the post-Batch-6 reachability scan is **not** claimed as a fresh pass.

The correct release evidence remains a clean hosted run that executes the full normal/race/security/benchmark matrices, preserves timeout/process diagnostics, and runs the new clean-export, SBOM, attestation, native package, install, launch, accessibility, and performance gates.

## Finding-by-finding reconciliation

- Architecture remediated in cited surfaces: **AR-014, AR-016**. Partial/contained: **AR-001–007, AR-009, AR-010, AR-012, AR-013, AR-015**. Open: **AR-008, AR-011**.
- Backend remediated in cited surfaces: **BE-001, BE-003–017, BE-019–022, BE-024–026, BE-028–031, BE-034–038, BE-040, BE-042**. Partial/contained: **BE-002, BE-018, BE-023, BE-027, BE-032, BE-033, BE-039, BE-041**. Open: **none**.
- Frontend remediated in cited surfaces: **FE-001–002, FE-005, FE-008, FE-010, FE-013–016, FE-018–031, FE-033–037, FE-039, FE-041–044**. Partial/contained: **FE-003–004, FE-006–007, FE-009, FE-011–012, FE-017, FE-017A, FE-032, FE-038, FE-040**. Open: **none**.
- Delivery/quality remediated in source/cited surfaces: **QR-005, QR-008–009, QR-015–019, QR-021–023, QR-026–028, QR-030–032, QR-035–036, QR-039**. Partial/contained: **QR-001–004, QR-006, QR-010–014, QR-020, QR-024–025, QR-029, QR-033–034, QR-037–038, QR-040**. Open: **none**.

## Highest-priority work still outstanding

1. **AR-008 — consolidate the duplicate data-transformation stacks.** Shared parser/escaping/output/cancellation contracts and one implementation per operation are still absent.
2. **AR-011 — decompose orchestration monoliths.** Workspace, Agent, DB, settings, and global renderer state still combine too many privilege and lifecycle decisions for reliable local reasoning.
3. **Global lifecycle and executor invariants.** Finish one resource registry and one backend-authoritative executor across editors, Big File, tables, transforms, DB, watcher, terminal, Agent, jobs, artifacts, workspace transition, and application shutdown.
4. **Authenticated durable recovery and cross-process commit.** Add identity-bound authenticated journals, complete crash reconciliation, cross-process locks/CAS, descriptor-based path authority, and metadata guarantees.
5. **Big File parser/acquisition completion.** Replace contiguous SQL table ranges with multi-span ownership and replace post-read `encoding/csv` limits with acquisition-time bounded parsing/external spill or explicit refusal budgets.
6. **Hosted/native release proof.** Run clean CI/security/performance evidence; choose a license; enable private reporting; sign/notarize supported artifacts; verify SBOM/provenance/checksums; and execute native install/launch/quit/accessibility suites.

## Release decision

Batch 6 closes the remaining bounded frontend bugs, moves the two open backend findings into materially safer partial states, adds missing governance/performance/supply-chain enforcement, and leaves only AR-008 and AR-011 wholly open. The review is nevertheless **not done as a production-readiness program**: 51 partial records still contain high-impact architectural or external acceptance criteria. Public release remains frozen.

## Later focused synchronization

The 2026-07-23 Quarry-derived large-file tool comparison, subsequent ports, and
their own validation/residual record are documented in
[12-quarry-tool-sync.md](12-quarry-tool-sync.md). That later pass intentionally
does not rewrite Batch 6's historical 89/51/2 reconciliation or imply that the
release freeze has been lifted.
