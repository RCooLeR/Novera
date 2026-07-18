# Remediation Progress — Batch 2

Date: **2026-07-17**

Reviewed baseline and current committed `HEAD`: **`46c90bba35d41a74d356f8ade7cb55999499c9c7`**

Local annotated safety tag: **`pre-remediation-2026-07-16`**

Status: **second uncommitted implementation batch; the full review is not closed**

Continuation: [08-remediation-progress-batch-3.md](08-remediation-progress-batch-3.md)

## Scope and status language

This document continues [06-remediation-progress.md](06-remediation-progress.md). The original 142 finding records remain the historical review of the tagged revision. This file records uncommitted working-tree remediation and does not delete, renumber, or retroactively rewrite those findings.

- **Remediated in cited surfaces** means the concrete vulnerable paths named below now enforce the stated invariant and have regression coverage. It does not close related findings in other services.
- **Contained** means an unsafe behavior is disabled or materially bounded while a complete replacement design remains outstanding.
- **Partially addressed** means risk was reduced, but the full finding acceptance criteria are not yet met.

## Implemented changes

| Findings | Status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| QR-003, QR-004, QR-005 | **Contained** | The tag-triggered release workflow now has an explicit failing `release-gate`. Every packaging matrix job requires that gate, and publication requires packaging, so a tag cannot currently build or publish an unvalidated release. YAML parsing and dependency assertions pass. | Publication is intentionally disabled, not release-ready. Replace the failing gate only after reusable exact-commit validation, tag/runtime version identity, signing/notarization, checksums, SBOM, and provenance/attestation verification exist. |
| BE-001 | **Remediated in the current DB, LLM, Agent, and renderer-secret surfaces** | LLM references are derived from provider plus normalized origin. DB references are derived from profile ID plus connection and TLS scope. Caller-supplied references are ignored or rejected, destination/security changes detach credentials or require password re-entry, and point-of-use checks re-derive ownership. Renderer secret APIs cannot enumerate or mutate foreign references. Only exact allowlisted historical DB/LLM references migrate; cross-purpose, cross-profile, and unknown references fail closed. Adversarial tests cover DB→LLM, LLM→DB, profile substitution, endpoint carry-over, legacy migration, and point-of-use foreign references. | Opaque references still appear in models as non-authoritative stored-state indicators. Every future secret consumer must use the same backend-owned purpose/scope rule. DNS resolution and rebinding are separate from textual origin binding and remain BE-006. |
| BE-023, AR-007 | **Strongly contained; not crash-atomic** | Secret `Set`/`Delete` now use copy-on-write memory updates only after durable-store success. DB profile save/delete and LLM settings/key changes use compensating rollback when a returned persistence error occurs. Missing-profile deletion now fails explicitly, and frontend settings reload from backend-authoritative state after key/config changes. Focused failure-injection, race, and vet suites pass. | Secrets and profiles/settings still live in separate physical stores. Power loss or process termination between commits can leave an inaccessible orphan. Full closure requires a durable intent journal/two-phase recovery or one transactional store, plus explicit recovery UI. BE-024 corrupt-master-key handling remains separate. |
| FE-006, FE-009, BE-027, AR-003 | **Partially addressed for workspace open/close, tree/file reads, and editor saves** | A transition epoch advances when switching starts, before the backend root changes. Open/Close requests are serialized; intermediate queued intents coalesce; only the latest intent commits UI state. New workspace I/O and watches are blocked during transition. Tree, file, reload, and save completions require matching workspace/tab identities. Failed latest Open restores the prior backend root and preserves dirty tabs. Backend Open/Close are serialized and `ListDir`, `ReadFile`, `ReadFileRange`, and the existing write path validate captured root generation. Deterministic A→B→C, delayed-response, failed-restore, and stale-save tests pass, including workspace race tests. | There is no bridge-visible opaque operation token or general cancellation/await protocol. Git is now root-pinned per call, but table/artifact operations, terminal, Big File, jobs, agent runs, watcher-event provenance, and global dirty-resource confirmation still need the full lifecycle coordinator. A started stale Open can transiently move the backend root while UI I/O is blocked before the queued latest intent restores the final root. |
| BE-004, AR-002 | **Remediated for Jobs producer methods only** | `Start`, `Append`, and `Finish` are no longer exported methods on the bound Jobs service. They are package producer functions backed by unexported methods. Agent and Workspace callers were migrated. The generated Jobs binding now exposes only `CancelJob`, `ClearFinished`, `GetJob`, and `ListJobs`. Reflection and generated-TypeScript allowlist tests fail on accidental method exposure. | Workspace `ReadRaw`/`WriteRaw` and other low-level concrete-service methods remain bound. Watcher `Suppress` is another producer-like surface. Every bound service still needs an intentional facade/allowlist review before approval controls can be treated as a security boundary. |
| BE-002 | **Further contained; explicit recovery remains disabled at product level** | Ordinary Big File open still never auto-replays `.qrp`. The internal parser now bounds physical journal size, entry count, aggregate rollback bytes, and per-entry allocation; validates flag, recorded size, offsets, overflow, ordering, overlap, truncation, and trailing bytes before opening the source for write; rejects changed source size; preserves corrupt/failing evidence; rejects lexical, case, symlink, and hard-link source/journal aliases; uses `O_EXCL`; and syncs sidecar parent-directory creation/removal on supported non-Windows platforms. A malicious corpus and alias regressions pass with race detection. | There is no production `Recover` caller. Do not expose one yet: the journal still lacks authenticated integrity, an operation nonce, strong source fingerprint/identity binding, expected-new-byte validation, preview/confirmation, and same-size replacement protection. Apply/Recover path ABA, concurrent replay, external-write drift windows, Windows directory durability/DACL policy, and crash-point fault injection remain open. |
| BE-037 | **Remediated for the confirmed shutdown race and hidden watch failures** | The watcher loop receives an immutable backend pointer. Shutdown uses `sync.Once`, signals and closes exactly once, waits for the loop, and only then clears state. `Watch` returns resolve/add/remove failures; failed parents are excluded from watched state. Initialization/runtime errors are retained, logged, returned, emitted as `fs:watch-error`, and displayed by the frontend status surface. Race tests repeated the shutdown path. | A fatal backend/channel failure is sticky and visible but does not automatically recreate the OS watcher. The current bridge returns aggregate errors rather than structured per-path status. Fire-and-forget caller cleanup and the bound `Suppress` surface remain separate work. |
| BE-038; BE-027 hardening in Git | **Remediated for the cited false-clean and unbounded-acquisition paths; lifecycle partially addressed** | Git stdout/stderr are captured through bounded writers during execution. Working-tree files are stat-checked and limit-read before diff rendering; `git show` and unified diffs acquire at their limits. Porcelain changes are count-capped before DTO amplification. Status/branch/head/stat/show failures are distinguished instead of becoming clean/absent results; expected non-repository/missing-object states remain explicit. One captured workspace root is used for every command in a public operation, so validation under A cannot execute under B. Confirmed commits retain `committed=true` if HEAD/status refresh fails. Unborn repositories use a cached empty-tree diff path. UTF-8 truncation is repaired, and `WaitDelay` bounds inherited-pipe waits after cancellation. Unit, deterministic subprocess, and race tests pass. | Root pinning prevents cross-workspace B mutation but does not cancel stale work against the old A root or validate a backend generation. Hooks/descendants are not yet terminated through a process-tree/job-object abstraction; `WaitDelay` bounds waiting, not descendant lifetime. Stage/unstage can still return a refresh error after a successful index mutation. Real-repository lifecycle coverage should be expanded beyond the deterministic helper corpus. |

## Independent adversarial re-review

Two focused read-only re-reviews were performed after implementation rather than relying only on author tests.

The recovery review found a latent `Recover(path, path)` deletion path and equivalent filesystem aliases. Those cases are now rejected and covered. The same review confirmed the new parser bounds and parse-before-write ordering, while identifying the remaining lack of authentication, same-size source identity, path-ABA protection, Windows directory durability, and concurrent fault-injection coverage. Those residuals are explicitly retained above; recovery remains unavailable through the product workflow.

The Git review found root recapture across commands, post-commit ambiguity, a near-deadline success race, status DTO amplification, unborn-HEAD failure, unexpected `git show` error masking, and UTF-8 boundary problems. These were addressed in the follow-up. The review also identified the remaining process-tree termination and stale-old-root cancellation work, which is not claimed fixed.

## Validation evidence

### Passed

- Safety tag dereference and current committed `HEAD` both resolve to `46c90bba35d41a74d356f8ade7cb55999499c9c7`; all remediation remains uncommitted.
- `go test ./... -count=1` passed across the full repository.
- `go vet ./...` passed.
- The integrated focused suite for Secret, Settings, DB, Jobs, Watcher, Workspace, recovery, Git, provider HTTP, LLM, Agent, and SQL guard passed.
- Focused race tests passed for Secret/Settings/DB, Workspace, Watcher, Git, and in-place recovery. Jobs/Workspace/Agent focused tests also passed after the bridge narrowing.
- Frontend Vitest: **3 files, 21 tests passed**.
- Frontend TypeScript typecheck and full ESLint passed.
- Production Vite build passed. The pre-existing Monaco/worker chunk-size warning remains.
- Both Taskfiles and the release workflow parse as YAML. Structural assertions confirm package jobs require `release-gate` and `build/docker/Dockerfile.server` is absent.
- `git diff --check` passed; the only output is the repository's existing Windows LF→CRLF warning.

### Validation note

An initial Vite invocation used a `--configLoader` option unsupported by the installed Vite 5 CLI and failed before build work began. The supported `vite build` command was then run and passed. This is a command-version mismatch, not a product-build failure.

## Highest-priority work still open

This batch materially reduces P0/P1 risk, but it does **not** make Novera release-ready. The next highest-value work is:

1. BE-034, FE-022, and AR-015: bind one-time approval to the exact canonical full operation, without truncated-preview mismatch.
2. FE-003 through FE-005, FE-011/FE-013, and AR-006: one dirty-resource coordinator for switch, close, quit, delete, staged Big File edits, and discard confirmation.
3. BE-005, BE-006, BE-010, BE-011, BE-026, and AR-004: verified agent transport, dial-time address policy, cancel/await semantics, process-tree termination, correct audit outcomes, and live-job retention.
4. BE-002: design an authenticated, identity-bound, user-visible recovery format/workflow before re-enabling any replay entry point.
5. BE-004: replace remaining broad Workspace/Watcher and other concrete bindings with narrow allowlisted facades.
6. BE-014, BE-030, BE-033, and BE-041: extend transactional output, manifest durability, metadata policy, and cross-process commit semantics beyond the paths already fixed.
7. QR-003 through QR-006: implement the actual exact-commit validation/version/signing/SBOM/provenance pipeline before removing the release freeze.
8. BE-024/BE-025 and AR-007: preserve and surface every corrupt persistent store with versioned migrations and recovery diagnostics.

The original review finding count remains **142 records**. Re-review remains required for every changed trust, recovery, secret, lifecycle, and release boundary before findings are formally closed.
