# Full Project Review and Dependency Remediation — 2026-08-17

Date: **2026-08-17**

Branch: **codex/quarry-tool-sync**

Committed baseline: **2a3ae0e229f618f355c5b9922589925bb8e11f42**

Status: **reviewed and materially hardened; public release remains frozen**

## Evidence boundary

This is an additive review of the current working tree. The checkout already
contained a large intentional remediation set before this pass began
(141 modified and 124 untracked paths, primarily the Quarry-derived Big File
work). Those changes were preserved and reviewed in place; this report does not
claim authorship of them or recompute the historical 142-finding ledger.

The original review and its conservative **89 remediated / 51 partial / 2 open**
reconciliation remain in
[00-review-index-and-executive-summary.md](00-review-index-and-executive-summary.md).
The Quarry synchronization evidence remains in
[12-quarry-tool-sync.md](12-quarry-tool-sync.md).

This pass covered:

- Go services, lifecycle, cancellation, resource bounds, persistence, and bridge
  exposure;
- React/Zustand state ownership, destructive-close paths, database queries,
  agent approvals, and malformed bridge data;
- Go and npm dependency graphs, reachable vulnerability evidence, Wails runtime
  coordination, generated bindings, and production builds;
- CI/security/release workflows, action/tool pins, Docker/Zig inputs, project
  metadata, documentation, and repository hygiene.

## Decision

The working tree is substantially safer and its current Windows production
binary compiles successfully. The dependency graph no longer contains the six
reachable Go vulnerabilities or four npm advisories found at the start of this
pass. The highest-risk newly identified data-loss, secret-persistence, shutdown,
and stale-request defects have regression coverage.

The repository is still **not release-ready**. Publication should remain
disabled until the residual architecture, native-platform, signing, version
identity, licensing, and hosted-evidence gates below are closed.

## Findings fixed in this pass

| Priority | Finding | Remediation and evidence |
| --- | --- | --- |
| High | Native close could beat the asynchronous dirty-state mirror and lose a just-made edit | Native close and quit now fail closed on the first attempt, issue a cryptographic nonce, and accept only a matching, unexpired decision read from the live Zustand store. Dirty, malformed, stale, timed-out, entropy-failure, and bridge-error paths remain open. Root tests and focused frontend tests cover the handshake. |
| High | Big File discard raced an already-dispatched StageEdit and could reset the single-owner edit session concurrently | DiscardEdits now shares the backend edit-work lock with StageEdit. A race-detector regression starts both operations together and proves the final edit count is zero. |
| High | Escape/backdrop could cancel a destructive discard in flight, leaving renderer dirty state inconsistent with a released backend session | Close-attempt cancellation is ignored while save/discard owns the attempt. Successful backend discard reconciles the exact tab/session instance before token validation. A deferred-discard frontend regression proves the modal remains owned and closes cleanly. |
| High | Agent audit JSONL stored raw command, SQL, query, result, error, URL credential/path, and other tool data | Audit writers now accept structured metadata only. Commands, SQL, queries, results, errors, URL credentials/path/query/fragment, headers, and bodies are omitted. New entries carry format version 2; the audit API refuses to surface legacy free-form summaries/details. Regression markers prove secrets do not enter new JSONL records. |
| High | Agent work survived Wails shutdown and pending approvals/commands were not drained | Agent admission now closes permanently at shutdown, pending starts are tracked, active contexts and approvals are canceled, and run leases are awaited with a bounded deadline. Tests cover a blocked start, an approval wait, idempotent shutdown, and command-process-tree cancellation. |
| High | Agent history was count-bounded but not byte-bounded and duplicate-call tracking grew for up to 10,000 steps | Backend policy now limits prompts (256 KiB), message payloads (1 MiB), tool arguments (256 KiB), tool calls per message (32), retained transcript (16 MiB), selection text (64 KiB), and duplicate signatures (4,096 digests). Provider messages are rejected before dispatch or transcript retention when they exceed a bound. |
| High | Terminal Start could register a shell after ServiceShutdown took its session snapshot | Starts reserve lifecycle ownership before root/PTY/process work, recheck the permanent stopping gate, and publish only while still admitted. A losing start kills its process tree, closes handles, reaps the process, and releases the reservation. Shutdown waits up to five seconds for pending starts. Tests cover timeout, late cleanup, and shutdown after process creation. |
| Medium | Database query admission relied on React render state; re-entry could submit twice and stale completion could overwrite a newer result | DbQueryRequestOwner provides synchronous single-flight ownership and generation-gated success/error/finally publication, with unmount invalidation and focused tests. |
| Medium | A model could provide valid JSON with non-string tool-card fields and crash Assistant rendering | Tool arguments are parsed as unknown, require a plain object, and validate known display fields before formatting. Malformed previews show an alert and cannot be approved; Deny remains available for a valid canonical intent. |
| Medium | Root bridge-boundary tests could race deletion of a workspace-local Go temp tree | The walker now skips every .gotmp-prefixed directory before descending. Ten focused repetitions passed. |
| Delivery | The security workflow referenced a nonexistent CodeQL action commit | CodeQL init/analyze now pin v4.37.7 at ff2f1c621b7f889edc0d3c761ac2e6a3f8cdb0dd; artifact attestation pins v4.2.2. actionlint passes. |
| Delivery | Every accepted prerelease tag identified itself internally as stable | Release identity now derives prerelease whenever the SemVer tag has a prerelease suffix. |
| Delivery | An unused Taskfile action fetched and executed CSS from mutable GitHub main content | The unreferenced Puppertino vendoring/mutation task was removed. |

## Dependency and toolchain updates

### Go

| Component | Before | After |
| --- | --- | --- |
| Toolchain | 1.26.5 | **1.26.6** |
| filepath-securejoin | 0.6.1 | **0.7.0** |
| Wails v3 | 3.0.0-alpha.79 | **3.0.0-beta.9** |
| Excelize | 2.10.1 | **2.11.0** |
| modernc SQLite | 1.52.0 | **1.56.0** |
| x/sys | 0.44.0 | **0.47.0** |
| x/text | 0.37.0 | **0.41.0** |

Minimum-version selection also moved x/crypto to 0.55.0, x/net to
0.58.0, x/sync to 0.22.0, modernc libc to 1.74.4, go-isatty to
0.0.24, and mscfb to 1.0.7. go mod verify passes and go mod tidy
-diff is empty.

The initial reachability scan found:

- standard library: GO-2026-6218, GO-2026-6090, GO-2026-6088, and
  GO-2026-5972, fixed by Go 1.26.6;
- x/text: GO-2026-5970, fixed from 0.39.0;
- x/net: GO-2026-5026, fixed from 0.55.0.

The selected graph exceeds every published fixed-version floor.

### Frontend

The coordinated frontend update includes:

- Wails runtime 3.0.0-beta.9;
- React/React DOM 19.2.8;
- xterm 6.0.0 and current fit/web-links addons;
- Monaco 0.56.0, Lucide 1.31.0, and Zustand 5.0.15;
- ESLint 10.8.1, TypeScript 6.0.3, typescript-eslint 8.67.0,
  Vite 8.2.1, and Vitest 4.1.10;
- DOMPurify 3.4.13 through an override for Monaco's vulnerable exact
  transitive pin.

The initial npm graph had three high and one moderate advisory. The final
installed graph reports **zero vulnerabilities** at the moderate threshold.
npm outdated reports only TypeScript 7.0.2, which is intentionally deferred
because typescript-eslint 8.67.0 supports TypeScript versions below 6.1.

### Build and CI tooling

- Node CI pin: 24.19.0
- Task: 3.52.0
- govulncheck: 1.7.0
- GoReleaser: exact 2.17.1
- Zig: 0.14.1, with corrected archive names and verified amd64/arm64 hashes
- Docker builder: Go 1.26.6 Bookworm at
  sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36

Wails' Go module, frontend runtime, CLI, generated bindings, documentation, and
workflow pins were moved together. The official beta.9 generator refreshed the
bindings and the bridge allowlist/method-ID tests pass.

## Final validation evidence

| Check | Result |
| --- | --- |
| Frontend TypeScript | passed |
| Frontend ESLint | passed |
| Frontend Vitest | **35 files / 223 tests passed** |
| Frontend production build | passed; 3,196 modules transformed |
| Production CSP and lazy-entry assertions | passed |
| npm audit (moderate threshold, system CA) | **0 vulnerabilities** |
| npm ls --depth=0 | clean |
| go mod verify | passed |
| go mod tidy -diff | clean |
| go vet -p=1 on root and internal packages | passed |
| go vet -p=1 with production tag on root | passed |
| Root tests on final combined tree | passed |
| Agent tests / race / vet | passed |
| Terminal tests / race (20 repetitions) / vet | passed |
| Focused Big File stage-discard race test | passed under the race detector |
| Production Go build with embedded final frontend | passed |
| Earlier coordinated Wails beta.9 Windows task build | passed |
| Generated bridge allowlist, Wails method IDs, and import boundary | passed |
| gofmt, git diff --check, and actionlint | passed |

The updated dependency candidate passed all root/internal tests before the final
lifecycle patches. A final broad combined run passed the root package, then
Windows security tooling suspended newly launched internal test executables at
zero CPU. The run was stopped and is **not** represented as a final broad pass.
Every package changed by the final lifecycle work passed its focused normal,
race, and vet suites.

The same host suspended the fresh govulncheck executable even for its version
command. The final reachability result is therefore **inconclusive on this
Windows host**, not silently called clean. The scanner is pinned in CI, and the
selected versions meet every fixed-version floor from the successful initial
scan.

## Residual risks and release blockers

### High-priority architecture and data safety

1. **One application-wide executor/lifecycle authority is still absent.**
   Big File, Agent, terminal, editor, database, watcher, workspace, and artifact
   operations still have separate ownership models.
2. **Platform-relative atomic authority remains incomplete.** Output
   publication still needs descriptor/handle-relative create, validate, fsync,
   rename, cross-process locking, and adversarial parent-substitution evidence.
3. **Durable authenticated recovery remains incomplete.** Current recovery is
   intentionally inspection-first; a writable design still needs authenticated
   source/backup identity and complete crash reconciliation.
4. **The duplicate transformation stacks and orchestration monoliths remain.**
   The historical AR-008 and AR-011 records are still open.

### Concrete backend follow-ups

1. Big File session identity deduplication can return a refreshed path that no
   longer matches the candidate identity during a transition.
2. A watcher runtime/channel failure remains latched and can poison every later
   workspace watch instead of rebuilding the backend.
3. LLM service admission still needs aggregate request bounds, a concurrent
   stream cap, and cancel-and-drain shutdown.
4. Existing live terminal sessions still use a synchronous, potentially
   unbounded process wait during shutdown. Windows retains the
   CreateProcess-to-job-assignment gap; deliberately detached Unix descendants
   can escape process-group kill.

### Release and governance

1. Release publication remains deliberately disabled in the release workflow.
2. Tag-derived version identity is not yet propagated into every fixed Windows,
   macOS, Linux package, and Wails metadata file.
3. Authenticode, Developer ID/notarization, and a Linux signing strategy remain
   absent.
4. The owner must select a project license and ship generated third-party
   notices. The Inter font's OFL is not a project license.
5. The optional NSIS task still obtains the WebView2 bootstrapper dynamically;
   do not rely on that packaging path until the input is pinned and verified.
6. Breaking GitHub Action majors were not mixed into this pass and require
   separate migration evidence.

### Native/runtime evidence

Wails v3 beta.9 remains pre-stable. Windows compilation and source-level
regressions pass, but interactive close/quit, native dialogs, packaging,
installation, signing, accessibility, performance, and runtime smoke must still
run on supported Windows, macOS, and Linux hosts before release.

### Operational notes

- Existing agent-audit.jsonl files created by older builds may still contain
  historical plaintext payloads on disk. The upgraded API no longer surfaces
  those legacy fields, but it intentionally does not delete user audit history.
  Users who previously ran Agent tools should review and, if appropriate,
  manually remove that file from the Novera user-config directory.
- Monaco language workers and editor chunks still trigger Vite's 500 KiB
  warning. They are lazy-loaded and the entry-laziness assertion passes, but
  product-level startup/memory budgets still need hosted measurements.

## Recommended next sequence

1. Run the pinned CI/security workflow on a clean Linux runner and retain
   govulncheck, CodeQL, dependency-review, secret-scan, race, and build evidence.
2. Fix LLM admission/shutdown, watcher reconstruction, and Big File transition
   identity before expanding features.
3. Build the shared executor/resource lifecycle and platform-relative atomic
   publication primitives.
4. Propagate tag identity into platform metadata, choose the license/notices
   policy, and implement signing/notarization.
5. Execute the native three-platform install/launch/edit/close/quit/dialog,
   accessibility, and performance matrix required by the Wails runtime policy.
6. Only then reconsider the release gate.
