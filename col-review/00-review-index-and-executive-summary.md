# Novera Full Project Review — Index and Executive Summary

Review date: **2026-07-16**
Reviewed revision: **`46c90bba35d41a74d356f8ade7cb55999499c9c7` (`v0.1.0`, `master`)**
Review output: documentation only under `col-review/`; product source was not changed.

Post-review implementation is tracked separately in [06-remediation-progress.md](06-remediation-progress.md) through [11-remediation-progress-batch-6.md](11-remediation-progress-batch-6.md). The later Quarry-derived large-file tool synchronization and focused re-review are recorded in [12-quarry-tool-sync.md](12-quarry-tool-sync.md). The 2026-08-17 full-tree review and dependency update are recorded in [13-full-project-review-2026-08-17.md](13-full-project-review-2026-08-17.md), with the immediate independent residual pass in [14-second-pass-hardening-2026-08-17.md](14-second-pass-hardening-2026-08-17.md). The original evidence and severity counts below remain the assessment of the reviewed revision; the later passes do not recompute those historical counts. The latest conservative historical reconciliation remains **89 remediated in cited surfaces, 51 partial/contained, and 2 open; release remains frozen**.

## Overall assessment

Novera is an ambitious, unusually feature-rich local-first workbench with several strong foundations: centralized workspace containment, revision-aware atomic editor writes, generated Go/TypeScript bindings, broad large-file unit coverage, bounded job logs, a CSP baseline, native builds on three desktop platforms, and deliberate approval/audit concepts for agent mutations.

It is **not ready to be represented as data-safe or release-ready at the reviewed revision**. The review found deterministic or directly evidenced paths that can:

- destroy a transform’s source when input and output identify the same file;
- mark unsaved edits as saved or close them after a failed save;
- lose invisible staged large-file edits on close or follow-tail;
- replay an unauthenticated adjacent recovery sidecar into a source file;
- use a caller-selected secret reference as a confused-deputy credential exfiltration channel;
- let late operations and state from one workspace cross into another;
- approve only a truncated preview while executing the full hidden mutation;
- bypass SQL dangerous-function detection with quoted identifiers;
- accept unauthenticated remote database TLS defaults;
- buffer ostensibly bounded command, model, SQL, query, rollback, and log data before limits are applied;
- publish a future tag with stale embedded version metadata and without proving that the tagged commit passed validation;
- expose the whole desktop privilege graph remotely if the advertised but currently non-building server target is casually repaired.

**Recommended disposition:** stop public release/promotion; disable server/Docker and unsupported mobile/package targets; complete the P0 items in the remediation roadmap; then re-review the changed trust and data-integrity boundaries.

## Review documents

| Document | Purpose | Finding records |
| --- | --- | ---: |
| [01-architecture-and-cross-cutting-review.md](01-architecture-and-cross-cutting-review.md) | Trust boundaries, workspace/session identity, service exposure, jobs, transactions, persistence, event contracts, lifecycle, observability | 16 |
| [02-backend-security-and-reliability.md](02-backend-security-and-reliability.md) | Go backend, secrets, agent/LLM, filesystem/data integrity, big-file core, DB/SQL, terminal/Git/watcher/jobs, resource bounds | 42 |
| [03-frontend-ux-and-accessibility.md](03-frontend-ux-and-accessibility.md) | State races, edit lifecycle, big-file UI, database/Git/terminal/jobs, dialogs/keyboard/screen readers, layout, bundle/dependencies | 45 |
| [04-testing-build-release-and-maintainability.md](04-testing-build-release-and-maintainability.md) | Test evidence, CI false-green paths, server/mobile/package builds, versions, dependency/supply chain, signing, governance, performance | 39 |
| [05-prioritized-remediation-roadmap.md](05-prioritized-remediation-roadmap.md) | Ordered P0/P1/P2 work, dependencies, regression invariants, ownership, release gates | — |
| [06-remediation-progress.md](06-remediation-progress.md) | Post-review safety tag, implemented fixes, validation evidence, residual risks, and next priorities | — |
| [07-remediation-progress-batch-2.md](07-remediation-progress-batch-2.md) | Second remediation batch: release freeze, secret ownership, workspace epochs, bridge narrowing, recovery/Git/watcher hardening, independent re-review | — |
| [08-remediation-progress-batch-3.md](08-remediation-progress-batch-3.md) | Third remediation batch: Agent transport/cancellation/event ordering, corruption-safe persistent stores, secret wrong-key and legacy quarantine/re-entry, parser/job/model bounds, settings concurrency, repeated adversarial re-review | — |
| [09-remediation-progress-batch-4.md](09-remediation-progress-batch-4.md) | Fourth remediation batch: Big File session/output/budget safety, DB schema/query bounds, Git/watcher/terminal/Jobs ordering, credential-state reconciliation, build identity, CI/build determinism, truthful large-file/release documentation, final integrated validation | — |
| [10-remediation-progress-batch-5.md](10-remediation-progress-batch-5.md) | Fifth remediation batch and full re-review: exact approvals, dial/process ownership, SQL/CSV/chunk correctness, staged large-file lifecycle, accessibility/event typing, dependency/supply-chain gates, conservative 142-finding reconciliation, residual release blockers | — |
| [11-remediation-progress-batch-6.md](11-remediation-progress-batch-6.md) | Sixth remediation batch and residual review: native close/conflict safety, Big File generations/jobs/recovery, workspace-bound table caches, exact bridge contract, performance/governance/SBOM evidence, 89/51/2 reconciliation | — |
| [12-quarry-tool-sync.md](12-quarry-tool-sync.md) | Quarry-to-Novera large-file synchronization: exact source/session ownership, CSV and SQL parser/tool parity, serializer-aware WordPress dump replacement, focused validation, intentional divergences, and residual risks | — |
| [13-full-project-review-2026-08-17.md](13-full-project-review-2026-08-17.md) | Full current-tree review: dependency/security updates, native/Agent/terminal lifecycle hardening, frontend request/approval fixes, validation evidence, and residual release gates | — |
| [14-second-pass-hardening-2026-08-17.md](14-second-pass-hardening-2026-08-17.md) | Independent residual pass: LLM/watcher/session lifecycle, physical audit migration, bounded workspace/XLSX acquisition, frontend async ownership, malformed URL credentials, and AppImage pinning | — |

The four audit documents contain **142 finding records**. This is not 142 independent bugs: architecture and delivery records deliberately describe root causes or gates that overlap concrete backend/frontend manifestations.

| Severity | Architecture | Backend | Frontend | Delivery/quality | Total records |
| --- | ---: | ---: | ---: | ---: | ---: |
| Critical | 1 | 3 | 7 | 0 | **11** |
| High | 7 | 30 | 18 | 10 | **65** |
| Medium | 8 | 9 | 19 | 28 | **64** |
| Low | 0 | 0 | 1 | 1 | **2** |
| **Total** | **16** | **42** | **45** | **39** | **142** |

The architecture Critical is explicitly **conditional/latent**: server mode currently fails to compile, so the remote path is not claimed active in this revision. It becomes Critical if the existing server/Docker design is made buildable without a separate authenticated, authorized, sandboxed service graph.

## Critical finding register

### Backend and architecture

| ID | Finding | Why it is Critical | Immediate containment |
| --- | --- | --- | --- |
| AR-001 | Advertised server mode would expose the desktop privilege graph remotely | A network caller could reach arbitrary-workspace filesystem, terminal, agent, DB, and other same-user privileges if the build is repaired as designed | Remove/disable server and Docker tasks; do not “just upgrade Wails” |
| BE-001 | Caller-selected secret refs are ambient capabilities | Renderer calls can cause the backend to send another subsystem’s stored secret to a caller-selected LLM/DB endpoint | Stop accepting/reusing arbitrary refs; bind refs to backend-owned purpose/object/origin |
| BE-002 | Adjacent `.qrp` recovery is auto-replayed without authentication | Merely opening a file can mutate it from attacker/stale-controlled sidecar entries; errors and size/range risks compound impact | Disable automatic replay; preserve journal; require identity/integrity validation and user-visible recovery |
| BE-003 | Workspace transforms can truncate their own input | Same source/output opens the source and then `os.Create` truncates the same inode, sometimes returning success | Reject same identity immediately; use a common temp-plus-atomic output transaction |

### Frontend/data lifecycle

| ID | Finding | Why it is Critical | Immediate containment |
| --- | --- | --- | --- |
| FE-001 | Save marks newer unwritten edits as saved | Typing during an in-flight save can clear dirty state for text never written, enabling silent close loss | Track submitted edit revision/snapshot; only that revision becomes clean |
| FE-002 | Save & Close closes after failed save | Permission, disk, or revision conflict can discard the sole in-memory copy | Return typed save result; close only on confirmed save of intended revision |
| FE-003 | Switch/close/quit/delete bypass one unsaved-work coordinator | Multiple normal exit paths can discard dirty resources | Temporarily block unsafe paths; implement one asynchronous lifecycle coordinator |
| FE-004 | Staged large-file edits are invisible and destroyed on close | Backend staged content is not represented in tab dirty state | Surface staging as dirty resource and include it in every close/quit flow |
| FE-005 | Follow-tail discards staged large-file edits | A routine viewer refresh can silently replace staged content | Disable follow-tail/navigation while staged or require save/discard resolution |
| FE-006 | Workspace switching leaves old state and operations alive | Old results/resources can cross into the new workspace and edits can be cleared | Add workspace generation/context; cancel and await A before activating B |
| FE-007 | Data tools permit source/destination alias truncation | UI makes the backend’s deterministic same-file destruction reachable through normal selection | Reject in UI for defense in depth and in backend by actual file identity |

## P0/high-risk cluster map

The following clusters should be assigned and closed as invariants, not fixed as isolated call sites.

| Risk cluster | Principal findings | System invariant to establish |
| --- | --- | --- |
| Safe file output | BE-003, BE-014, BE-030, BE-033, FE-007, AR-005 | No output operation can damage an input identity; final output appears only after complete durable commit |
| Recovery/rollback | BE-002, BE-012, BE-013, BE-033 | Recovery is authenticated, bounded, identity-bound, idempotent, and never reported complete when partial |
| Exact edit lifecycle | FE-001 through FE-005, FE-011, FE-013, AR-006 | Dirty/staged state corresponds to an exact resource revision and blocks every destructive navigation/quit path |
| Workspace isolation | BE-027, FE-006, FE-009, FE-010, FE-012, FE-014/15/18/20/24/25, AR-003 | A request, event, resource, approval, or job from workspace A cannot affect workspace B |
| Secret ownership | BE-001, BE-023, BE-024, AR-007, AR-015 | A secret is usable only for its backend-owned object, purpose, and approved origin |
| Exact approvals | BE-034, FE-022, AR-015 | The exact canonical operation digest shown is the only operation that can execute once |
| Agent/LLM transport | BE-005, BE-006, BE-008, BE-009, BE-036, BE-042 | Credentials require verified transport; redirects/DNS are revalidated; bodies/logs/timeouts are bounded before buffering |
| SQL/DB safety | BE-007, BE-016, BE-021, BE-022, BE-035, FE-024 | Parser-aware validation, least privilege, verified TLS, server timeout, cancellation, and row/byte caps act independently |
| Job/cancellation ownership | BE-010, BE-011, BE-026, BE-032, FE-017A, FE-019, FE-028, AR-004 | One executor owns every long action, context, process tree, progress, output transaction, and terminal state |
| Public bridge surface | BE-004, AR-002 | Only intentional facade methods are bound; internal producers/raw primitives are absent from generated JavaScript |
| Release identity/evidence | QR-003 through QR-006, QR-016, QR-020, QR-034 | The exact tag commit passes reusable gates, builds cleanly, identifies itself correctly, and yields verified signed/attested artifacts |

## Highest-value first decisions

1. **Declare the supported product surface.** Keep Windows/macOS/Linux desktop; mark server/Docker/iOS/Android/MSIX/ARM64 cross paths disabled until each has an owner, threat model, and passing CI. Current server, iOS, Android, MSIX, and Linux ARM cross paths have concrete failures or misleading behavior.
2. **Freeze release publication.** The tag workflow currently does not rerun or depend on validation for the same commit, and future tag versions will not propagate into platform metadata.
3. **Build the output transaction first.** It removes the deterministic same-file loss class and gives transforms, big-file output, agent artifacts, and registries one failure/cancellation/durability contract.
4. **Build workspace/session and resource lifecycle primitives before feature fixes.** Many frontend races and lost-edit paths share those missing identities; local patches will recur without them.
5. **Reduce the RPC surface before expanding remote/provider features.** Generated bindings prove internal Jobs producer methods are callable despite comments saying otherwise.
6. **Make secrets and approvals object-bound capabilities.** UI confirmation cannot repair a backend that accepts arbitrary secret refs or executes more than the approved preview.
7. **Layer DB/network controls.** Do not rely on regex/token heuristics or “encrypted but unverified” TLS as security boundaries.
8. **Convert every fixed finding into an adversarial regression.** The existing happy-path tests pass while deterministic failures remain.

## Validation outcome summary

### Passed locally

- `go mod verify`
- `go mod tidy -diff` with no changes
- `go vet ./...`
- targeted Go package tests, including large-file and reviewed security/data packages
- frontend dependency graph resolution
- frontend TypeScript typecheck
- frontend ESLint
- frontend Vitest: 1 file / 5 tests
- frontend production build
- `npm audit --omit=dev`: zero production dependency advisories at review time

### Failed or incomplete

- Broad `go test ./...`: project packages reached passed except `internal/jobs`, whose Windows test executable was blocked by Microsoft Defender as possible unwanted software; the pattern also included a Go fixture from `frontend/node_modules`.
- Windows and Linux server-tag builds: failed inside pinned Wails alpha.79 through separate platform/interface errors.
- Full npm audit: five development-tool advisories (3 moderate, 1 high, 1 critical), principally Vite/Vitest/esbuild developer-server tooling.
- `govulncheck`: bounded source/binary/version invocations did not complete on this host, so Go vulnerability status remains unknown rather than clean.
- Broad Go coverage: attempts stalled/hit the same environmental behavior; no coverage percentage is claimed.

### Important interpretation

Passing unit/type/build checks show the repository is syntactically and structurally viable. They do **not** test the cross-layer sequences where the highest-impact bugs occur. The Defender result is also not evidence that `internal/jobs` is malicious; it is an unresolved environmental/signature problem that CI currently works around by omitting primary-platform coverage.

## Review scope

Reviewed areas include:

- application composition and Wails bridge exposure;
- workspace filesystem access, editor writes, transforms, table browsing, artifacts, recovery, and path containment;
- large-file document/session/edit/search/replace/export/plugin behavior;
- agent tool loop, approvals, audit, command/network/model boundaries, rollback, and jobs;
- LLM provider transport and event streaming;
- database profiles/secrets, DSNs/TLS, SQL guard, query limits, schema and UI;
- Git, terminal, watcher, jobs, settings, secret storage, and persistence;
- frontend global state, components, menus/dialogs, editor/table/big-file flows, async ownership, cleanup, clipboard, keyboard, screen-reader, layout, motion, and bundle loading;
- Go/npm manifests, CI/release workflows, Taskfiles, Dockerfiles, platform package metadata, documentation, repository hygiene, and governance.

Methods included static control/data-flow tracing, generated-binding inspection, targeted concurrency/security reasoning, source-level reproduction paths, bounded build/test/vet/audit commands, package/test inventory, bundle inspection, and independent fact-check passes over backend, frontend, architecture, and delivery findings.

## Limitations and blind spots

This was a deep repository review, not a certification or proof that every bug was found. In particular:

- No full interactive desktop usability session was completed on all three operating systems.
- No packaged Windows installer/MSIX, signed/notarized macOS app, Linux package, mobile app, or server container was successfully exercised end to end.
- No real PostgreSQL/MySQL TLS matrix or live external LLM/provider penetration test was run.
- No destructive fault-injection filesystem (disk-full, power-loss, rename/close/fsync failures) was available.
- Go race/fuzz/coverage runs were targeted/bounded rather than an unlimited all-package campaign.
- `govulncheck` did not return a usable result on the review host.
- Accessibility findings are source/interaction-model based; they should be validated with keyboard, screen readers, high contrast, zoom, reduced motion, and real platform webviews.
- Dependency advisory status is time-dependent and must be rescanned in CI/release.
- Server transport behavior was inspected in the pinned dependency, but the target itself does not compile; the Critical remote assessment is consequently conditional.
- Existing local Git object history/unreachable blobs were not treated as repository findings unless reachable from the reviewed source; no secret was established by the HEAD scan.

## Definition of review closure

The review should not be considered closed merely because issue tickets exist. Closure requires:

- every Critical and P0 High finding fixed or the affected feature removed/disabled;
- regression tests for the invariant, including aliases, concurrency, cancellation, malformed/unbounded input, and failure injection where relevant;
- a binding/event contract diff showing no accidental public API;
- a reusable validation workflow that the tag release invokes for the exact commit;
- platform/runtime version extraction matching the tag;
- clean-checkout builds with no tracked/untracked dependency on stale output;
- signed/notarized/attested release artifacts and an accurate license/support/documentation set;
- an independent focused re-review of output transactions, recovery, secrets, agent approvals/networking, SQL/DB controls, workspace lifecycle, and release automation.

The implementation order and detailed acceptance tests are in [05-prioritized-remediation-roadmap.md](05-prioritized-remediation-roadmap.md).
