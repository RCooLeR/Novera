# Second-Pass Project Hardening — 2026-08-17

Date: **2026-08-17**

Branch: **codex/quarry-tool-sync**

Committed baseline: **2a3ae0e229f618f355c5b9922589925bb8e11f42**

Status: **additional concrete residuals fixed; public release remains frozen**

## Evidence boundary

This is an additive follow-up to
[13-full-project-review-2026-08-17.md](13-full-project-review-2026-08-17.md).
The shared checkout already contained the large Quarry/remediation working tree
and the first review pass. All unrelated edits were preserved. This pass does
not recompute the historical 142-record ledger or claim that the remaining
architecture/release program is complete.

The pass independently revisited:

- the three concrete backend follow-ups named in report 13 (LLM lifecycle,
  watcher reconstruction, and Big File transition identity);
- physical audit-log privacy and retention rather than read-time masking alone;
- workspace file acquisition, archive expansion, and Agent raw-copy paths;
- frontend async ownership, workspace transitions, watcher recovery, malformed
  bridge/settings input, and keyboard/dialog accessibility;
- remaining build-script network inputs and AppImage packaging correctness.

## Decision

Every concrete backend follow-up listed in report 13 now has an implementation
and focused normal/race evidence. The second pass also found and fixed new
bounded-acquisition, plaintext-persistence, stale-response, and executable
supply-chain issues.

The repository is still **not release-ready**. The shared lifecycle/atomic
publication architecture, native platform evidence, signing, complete version
metadata, licensing/notices, and hosted security evidence remain release gates.

## Findings fixed in this pass

| Priority | Finding | Remediation and evidence |
| --- | --- | --- |
| High | LLM Send accepted large aggregate requests, admitted unlimited model-list calls, and did not cancel/drain work at application shutdown | Bridge-controlled and normalized messages are bounded before marshaling (256 messages, per-field ceilings, 4 MiB aggregate text, exact encoded ceiling). Admission permits four streams and two model-list calls; canceled work retains its slot until cleanup. Shutdown permanently closes admission, cancels pending setup/HTTP work, and drains for up to ten seconds. Deterministic normal/race tests cover boundaries, setup races, non-cooperative cleanup, idempotence, and late release. |
| High | A watcher runtime error or closed channel poisoned later watches; the current workspace also had no immediate renderer retry | Failed generations now retire once. The next Watch transactionally creates a backend, re-adds the complete desired directory plan, and commits only after success; factory/Add failures remain retryable and shutdown cannot resurrect a candidate. The renderer makes one coalesced recovery sync for runtime/unavailable errors, blocks recursive error loops, and rearms after a normal success or workspace replacement. Watcher race tests ran 100 repetitions and frontend coordinator tests cover success/failure/rearm. |
| High | Big File identity deduplication discarded the candidate descriptor while a matching session transitioned, then trusted a stale ID/path lookup | Registry.Open retains the candidate identity across the transition, rescans against the new generation, and revalidates before reuse. Single and four-concurrent hard-link regressions prove path A receives a distinct session after the retained session transitions to B. |
| High | Legacy Agent audit payloads were hidden by the API but remained physically in plaintext, and the journal had no retention ceiling | Startup now atomically rewrites legacy JSONL through the structured schema, removes free-form summary/detail and unknown fields, drops malformed/oversized records, secures directory/file modes, rejects linked/non-regular paths, and fails closed on migration error. The newest 4 MiB is retained at startup/compaction and live growth is capped at 8 MiB. Physical secret-marker and newest-window tests pass under the race detector. |
| High | Workspace Search, Diagnostics, editor reads, revision checks, and Agent copy/append used stat-then-unbounded-read patterns | A common opened-file helper validates the exact regular-file identity and uses a max+1 limiter during acquisition. Search/Diagnostics enforce 1 MiB after open, editor/revision reads enforce 64 MiB, and Agent append/copy use the 8 MiB rollback budget. The unused unbounded ReadRaw primitive was removed. Sparse-file, linked-path, exact-bound, copy-refusal, normal, and race tests pass. |
| High | XLSX preview checked only compressed size, while Excelize's default aggregate expansion ceiling was 16 GiB and header metadata alone was not a sufficient bomb defense | Workbooks are acquired as an immutable, identity-checked 50 MiB snapshot. An independent ZIP preflight streams every entry and enforces actual 256 MiB expansion plus 8,192 entries, including forged size metadata; Excelize then receives explicit 256 MiB aggregate/16 MiB XML-memory options. Tests cover compressed, expanded, entry-count, and forged-header rejection. |
| High | Malformed provider URLs could preserve pasted user:password text in plaintext settings when URL parsing failed | Renderer and backend now remove unambiguous authority userinfo even on malformed hierarchical URLs. The backend persistence regression reads settings.json and proves the secret marker is absent; valid path at-signs and non-URL text are not rewritten. |
| Medium | Jobs, artifacts, audit refreshes, and workspace search could publish older responses after a newer request/dialog/workspace state | Generation ownership now gates publication; close/query changes invalidate ownership synchronously. Focused deferred-promise tests prove newest-only results and reject cross-workspace/dialog-close completion. |
| Medium | Watcher/runtime transitions left job/artifact controls, assistant drafts, Git commit text, and navigation diagnostics able to act or appear in the wrong workspace | Actions capture workspace identity, controls disable during transitions, late status/navigation is suppressed, and workspace-scoped unsubmitted Assistant/Git state remounts only after the authoritative root changes. |
| Medium | Command palette lacked a dialog focus trap and complete combobox/listbox semantics | It now uses the shared dialog focus lifecycle, modal/combobox/listbox roles, active-descendant ownership, selected-option state, and labeled controls. |
| Delivery | AppImage packaging downloaded and executed mutable linuxdeploy `continuous` assets, treated every non-x86 host as ARM64, and quoted the generated-output glob so it could never expand | The script pins upstream [`1-alpha-20251107-1`](https://github.com/linuxdeploy/linuxdeploy/releases/tag/1-alpha-20251107-1), verifies GitHub-published amd64/arm64 SHA-256 values, rejects unsupported architectures, and requires exactly one expanded output match. Bash syntax and diff checks pass. |

## Validation evidence

| Check | Result |
| --- | --- |
| Frontend Vitest | **38 files / 235 tests passed** |
| New frontend ownership/recovery/URL tests | **3 files / 12 tests passed** independently |
| Frontend TypeScript and ESLint | passed |
| Frontend production build, CSP, and lazy-entry assertions | passed |
| npm audit (moderate threshold, system CA) | **0 vulnerabilities** |
| npm ls --depth=0 | clean |
| npm outdated | only TypeScript 7.0.2; intentionally deferred while typescript-eslint supports versions below 6.1 |
| LLM normal tests / race / vet | passed |
| Watcher race (100 repetitions) / vet | passed |
| Big File session normal tests / focused race / vet | passed |
| Agent full normal suite | passed |
| New Agent audit/copy tests under race detector | passed |
| Workspace focused normal/race tests | passed |
| Settings malformed-URL normal/race tests | passed |
| Vet across Agent, workspace, LLM, watcher, Big File session, and settings | passed |
| go mod verify and go mod tidy -diff | passed / clean |
| Root production build with embedded final frontend | passed (51,474,432-byte Windows binary) |
| actionlint, bash -n, gofmt, and git diff --check | passed |

A final combined multi-package Go run again encountered the host behavior from
report 13: a newly built test executable remained at zero CPU with a minimal
working set until stopped. Agent completed before the stall; fresh full
workspace/settings executables also reproduced it, while focused normal/race
executables and all changed-package vet/build checks passed. Those suspended
runs are **inconclusive**, not reported as green.

The host still cannot provide a final govulncheck execution. CI remains pinned
to govulncheck 1.7.0, and report 13 records the successful initial reachability
scan plus selected fixed-version floors.

## Updated residual risks and release blockers

### Architecture and data integrity

1. One application-wide executor/resource lifecycle still does not own every
   workspace, editor, Big File, Agent, LLM, terminal, database, watcher, job,
   artifact, and output operation.
2. Descriptor/handle-relative atomic authority, parent-substitution resistance,
   cross-process locking, and complete crash durability remain platform-relative
   work. The new opened-file checks close ordinary final-identity/growth races,
   not the full parent-directory transaction problem.
3. Durable authenticated writable recovery and the historical duplicate
   transformation/orchestration stacks remain unresolved.
4. Existing live terminal sessions still perform a potentially unbounded
   synchronous wait during shutdown. Windows retains its process-create to Job
   assignment gap; deliberately detached Unix descendants can escape a process
   group.

### Bounded-service tradeoffs

1. A non-cooperative custom HTTP transport/settings/secret implementation may
   outlive the LLM ten-second drain timeout; admission remains permanently
   closed and late cleanup releases its lease safely.
2. Watch events lost during an OS overflow/rebuild cannot be replayed. Recovery
   depends on backend factory/Add/Close calls and platform watch quotas.
3. Big File identity still depends on filesystem identity quality; virtual or
   network filesystems may expose unstable IDs, and pathname rebinding after
   final admission is detected by later validation rather than made atomically
   impossible.
4. XLSX preflight deliberately spends bounded extra CPU/I/O by validating
   expansion before Excelize parses the same snapshot.

### Release and governance

1. Release publication remains deliberately disabled until the exact tag commit
   passes reusable validation.
2. Tag identity is not yet stamped into every Windows/macOS/Linux package and
   Wails metadata surface.
3. Authenticode, Developer ID/notarization, and the selected Linux signing
   strategy remain absent.
4. A project license and generated third-party notices still require owner
   decisions.
5. The optional NSIS path still obtains the WebView2 bootstrapper dynamically.
6. Wails beta.9 still requires interactive install/launch/edit/close/quit,
   dialog, accessibility, performance, and packaging evidence on all supported
   native platforms.

### Operational note

On a normal upgrade, the first Agent service initialization now sanitizes old
`agent-audit.jsonl` contents on disk rather than merely hiding them in the UI.
If the audit directory/file is linked, non-regular, or cannot be secured and
atomically replaced, auditing fails closed and logs the migration error. In that
exceptional case the old file remains untouched for the user to repair or
remove; Novera will not append to it.

## Recommended next sequence

1. Run the pinned CI/security workflow on a clean Linux host and retain broad
   tests, race, govulncheck, CodeQL, dependency-review, secret-scan, build, and
   packaging evidence.
2. Implement the shared executor and platform-relative atomic publication
   primitives, then move remaining services onto them.
3. Bound existing-terminal shutdown and complete crash/recovery identity work.
4. Stamp tag identity through all package metadata, choose license/notices and
   signing policies, and pin/verify the remaining NSIS input.
5. Execute the required three-platform native/runtime matrix before reconsidering
   the release gate.
