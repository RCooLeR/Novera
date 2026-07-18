# Remediation Progress — Batch 1

Date: **2026-07-16**
Reviewed baseline: **`46c90bba35d41a74d356f8ade7cb55999499c9c7`**
Local annotated safety tag: **`pre-remediation-2026-07-16`**
Status: **first implementation batch; the full review is not closed**

Continuation: [07-remediation-progress-batch-2.md](07-remediation-progress-batch-2.md)

## Snapshot and scope

The annotated tag resolves to the reviewed source revision and records the committed product tree immediately before remediation. The review documents were untracked working-tree files when the tag was created, so they are not stored in that tag. The tag is local until explicitly pushed.

This document tracks changes made after the reviewed snapshot. The original finding records, evidence, severity counts, and line references remain a historical review of the tagged revision and are intentionally not rewritten.

Status terms used below:

- **Remediated in cited paths** means the concrete vulnerable paths named by the finding now have code and regression coverage, but related roadmap clusters may still contain separate findings.
- **Contained** means the unsafe automatic or advertised behavior is disabled, while the complete replacement design remains outstanding.
- **Partially addressed** means risk was reduced without closing the finding's full invariant.

## Implemented changes

| Findings | Status | Implementation and proof | Important residual work |
| --- | --- | --- | --- |
| AR-001, QR-001, QR-002 | **Contained** | Server and server-Docker build/run tasks now fail through one explicit disabled task; the root task summaries identify the security reason; `build/docker/Dockerfile.server` was removed. Both Taskfiles parse as YAML. | There is still no authenticated, authorized, sandboxed server product. Do not restore a server tag, listener, image, or task without a separate composition root, threat model, and CI security tests. |
| BE-002 | **Contained** | `FileService.OpenFile` no longer calls recovery or mutates a source merely because an adjacent `.qrp` exists. Valid and malformed evidence is preserved. In-place saves create the sidecar with `O_EXCL` and return `ErrRecoveryPending` instead of overwriting prior evidence. Tests cover a crafted valid sidecar, malformed evidence, and SavePatch refusal. | Recovery still needs authenticated provenance, source identity/fingerprint binding, bounded parsing, preview/confirmation, idempotence, deliberate cleanup, and parent-directory durability. The internal `Recover` primitive is not itself an authorization boundary. |
| BE-003, FE-007 | **Remediated in the seven legacy Workspace transform paths** | CSV→SQL, dump extract/split/transform, CSV project/add-column, and dump-table→CSV now use one same-directory staged output. The helper pins the opened input identity, rejects normalized/case/canonical/symlink/hard-link aliases, rechecks before commit, syncs/closes, and atomically replaces only after success. Short byte ranges now fail without publishing partial data. Tests exercise every facade, aliases, preserved destinations, permissions, and premature EOF. | `SplitDump` commits atomically per output, not as one all-or-nothing set. ACLs, xattrs, ownership, and Windows DACL inheritance need an explicit metadata policy; parent-directory replacement remains a narrow TOCTOU without handle-relative APIs. Other producers named by BE-014/BE-030/BE-033 are not yet migrated. |
| FE-001, FE-002 | **Remediated for editor save and Save & Close** | Saves capture the submitted snapshot/revision, return a typed result, retain newer edits as dirty, and share a workspace-scoped per-path queue across normal and encoding saves. Stable tab/workspace identities plus monotonic request and close-attempt tokens reject obsolete responses, queued same-path reincarnations, established A→B workspace replacements, and cancel/reopen races. Save & Close closes only after a confirmed clean result; failures and newer edits keep the tab/dialog. Buttons are guarded during saving. Seventeen frontend tests now include deferred writes, repeated saves, encoding saves, stale/generic errors, tab/workspace reincarnation, cancel/reopen, and Save & Close. | The global dirty-resource coordinator remains missing (FE-003 through FE-006). `openWorkspace` still has a pre-identity-change blind interval and broader request-epoch race (FE-009); switching, close/discard during in-flight work, and stale non-save responses need one lifecycle policy. A save that commits after same-path close/reopen intentionally fails the new tab stale and retains its dirty content. |
| BE-041 | **Partially addressed** | `Workspace.WriteFile` now serializes the revision check and atomic commit across all calls to the service. It captures the backend root/generation before waiting, rejects queued calls after `Open`/`Close`, and never re-resolves a call that captured A against B. Deterministic tests prove one same-revision writer succeeds while the other receives `ErrStale`, and a blocked A→B sequence leaves B untouched. The frontend queue ensures a later save captures the successful predecessor's new revision. | External processes, other raw-write primitives, multiple service instances, semantically stale requests issued only after the backend has already switched roots, and writes already past the generation check remain outside the full lifecycle/CAS boundary. A cross-process commit protocol plus frontend/backend operation tokens and cancellation/await on workspace transition are still required. |
| BE-007 | **Remediated for the confirmed bypasses** | The SQL analyzer retains double-quoted, backtick, and bracket identifiers. Quoted dangerous function calls are checked by engine while quoted keyword columns remain usable. `SELECT … INTO` detection now distinguishes quoted identifiers from real clause boundaries. Regression tests cover schema-qualified and quoted functions plus both quoted-clause `SELECT INTO` bypass shapes. | This remains a heuristic tokenizer, not the dialect-aware positive-subset parser and least-privilege execution boundary required by the roadmap. |
| BE-008 | **Remediated in Agent provider diagnostics** | Raw response diagnostics are disabled by default. Explicit `NOVERA_AGENT_DEBUG_RESPONSES=1` opt-in stores only a redacted structural JSON preview; unknown fields and every scalar value are removed/redacted, non-JSON text is omitted, URLs lose userinfo/query/fragment, entries are preview-bounded, and logs rotate to one bounded backup. Startup removes current, backup, and legacy raw logs. Documentation and tests cover default-off, redaction, cleanup, and rotation. | The opt-in is environment-controlled rather than an in-app time-limited diagnostic session/clear action. Other application logs still require the broader structured redaction review. |
| BE-009 | **Remediated in Agent and LLM provider response paths** | A shared acquisition-time bounded reader rejects declared oversize before reading and detects one-byte overflow for unknown/chunked bodies. Agent completions are capped at 8 MiB, provider error bodies at 8 KiB, model lists at 2 MiB, and aggregate SSE streams at 32 MiB in addition to the existing 1 MiB scanner-record limit. `httptest` regressions cover exact-limit, declared oversize, chunked overflow, model parsing/sorting, normal SSE deltas, and oversized responses. | Fixed constants are not yet centrally configurable. Already-delivered stream deltas cannot be retracted when the aggregate limit is later exceeded. Missing SSE terminal-marker validation and the wider transport/DNS/redirect findings remain open. |
| BE-022 | **Remediated for empty/default remote modes** | Empty remote PostgreSQL mode now defaults to hostname-verifying `verify-full`; empty remote MySQL mode defaults to verified `tls=true`. Loopback compatibility remains. Explicit plaintext, fallback, unverified, and CA-without-hostname Postgres modes are logged; explicit unverified MySQL TLS is logged. Tests cover secure defaults and compatibility opt-outs. | Explicit insecure modes remain available by design. The UI still needs deliberate CA/client-certificate configuration and prominent warnings, followed by a live PostgreSQL/MySQL TLS matrix. |

## Validation evidence

### Passed

- The annotated tag resolves to `46c90bba35d41a74d356f8ade7cb55999499c9c7`.
- `go test ./internal/workspace -count=1` after the alias, permission, and editor-write-lock fixes.
- `go test ./internal/sqlguard -count=1` after the quoted-token and `SELECT INTO` audit fix.
- Focused recovery/FileService tests, the `internal/bigfile/inplace` suite, and vet on the affected packages passed in the implementation worker.
- `go test ./internal/providerhttp ./internal/llm ./internal/agent` and matching focused `go vet` passed after the final provider patch in the implementation worker.
- Frontend Vitest: **2 files, 17 tests passed**.
- Frontend TypeScript typecheck, ESLint, and production Vite build passed. The build retains the pre-existing Monaco chunk-size warning.
- Both Taskfiles parse successfully as YAML.
- `git diff --check` and Go formatting checks are clean.

### Host-limited checks

Microsoft Defender intermittently suspends generated Go test executables on this Windows host. Several independent reruns produced no test output until the command timeout, and one database run printed `ok` before failing only because the temporary `db.test.exe` remained locked during cleanup. Those orphaned Novera test processes were terminated after their command lines and build directories were verified. These events are treated as an unresolved host/CI limitation, not as passing or failing assertions.

The `task` executable also failed to return within the bounded validation window. The disabled-task wiring was therefore checked structurally and by YAML parsing, not by claiming a successful live `task build:server` invocation.

## Highest-priority work still open

The first batch does **not** make the project release-ready. Recommended next work remains:

1. BE-001 and the associated secret-ownership findings: replace caller-selected secret refs with backend-owned, purpose-bound capabilities.
2. FE-003 through FE-006 and AR-003/AR-006: one dirty-resource/workspace-generation coordinator covering switch, close, quit, delete, staged Big File state, and late operations.
3. BE-034, FE-022, and AR-015: canonical full-operation intents with one-time digest-bound approval.
4. BE-005, BE-006, BE-036, and BE-042: verified credential transport, dial-time address policy, redirect/DNS rebinding protection, and strict stream termination.
5. BE-004 and AR-002: reduce generated bridge bindings to intentional facades; remove internal producer/raw methods.
6. BE-010/BE-011/BE-026/BE-032 and AR-004: one executor/job/cancellation ownership model with process-tree termination and bounded output.
7. BE-014/BE-030/BE-033/BE-041: extend the output transaction and durability/metadata contract beyond the seven legacy transforms.
8. QR-003 through QR-006 and the Wave 4 release gates: freeze publication until the exact tag commit is validated, version-identical, signed, and attested.

Re-review is required after the remaining Critical/P0 clusters are implemented. Finding counts in the original review remain **142 records**; this progress file records implementation status rather than deleting or renumbering findings.
