# Architecture and Cross-Cutting Review

## Executive summary

Novera has a credible foundation for a local-first desktop workbench: Go owns privileged operations, generated bindings keep much of the TypeScript/Go contract aligned, workspace path resolution is centralized, and the large-file engine is decomposed into focused packages. The primary architectural problem is not a lack of features; it is that feature delivery has outgrown the lifecycle, authorization, and transaction boundaries around those features.

The review records **16 cross-cutting findings**: **1 Critical (conditional on enabling the advertised server target), 7 High, and 8 Medium**. These records intentionally describe system-level causes. Concrete manifestations are catalogued in [02-backend-security-and-reliability.md](02-backend-security-and-reliability.md) and [03-frontend-ux-and-accessibility.md](03-frontend-ux-and-accessibility.md).

The release-blocking architectural themes are:

1. The same privileged service graph is used as though it were a trusted desktop-only boundary, while the repository also advertises an HTTP server deployment. If that target is repaired without adding authentication and capability restrictions, it becomes a remote filesystem, terminal, database, Git, secret-consuming, and agent-control surface.
2. Workspace identity is a mutable singleton. Calls and events do not carry a workspace generation, so old asynchronous work can act on or commit into a newly selected workspace.
3. Long-running work is split among two incompatible job systems, synchronous calls, and fire-and-forget frontend actions. Cancellation, progress, persistence, ownership, and cleanup are therefore inconsistent.
4. File transformations, editor saves, artifact registration, and rollback do not share one durability contract. Several concrete data-loss defects follow from that fragmentation.
5. The application lacks a single dirty-resource/lifecycle coordinator for text tabs, table views, staged large-file edits, terminals, transforms, and agent activity.

The safest near-term direction is to explicitly support **desktop mode only**, reduce the bound RPC surface to deliberate DTO-oriented facade methods, introduce a workspace-scoped operation context with generation IDs, unify jobs and output transactions, and make all close/switch/quit paths flow through one lifecycle coordinator.

## Scope and method

This review traced the composition root, bound Wails services, generated TypeScript bindings, workspace and large-file service boundaries, frontend state/event plumbing, persistence locations, job/artifact integrations, build targets, and documentation claims. It also cross-correlated the backend and frontend audits so that recurring symptoms are attributed to their shared architectural cause.

Severity means:

- **Critical** — plausible remote compromise or catastrophic loss at a supported trust boundary; must block enabling or shipping that boundary.
- **High** — realistic loss of user data, credential misuse, cross-workspace corruption, or a fundamental lifecycle/control failure.
- **Medium** — substantial maintainability, diagnosability, correctness, or future-security debt that should be scheduled.

Confidence is high unless a finding explicitly says it is conditional. Line references describe the reviewed revision and should be rechecked after edits.

## Current system map

| Layer | Current implementation | Architectural observation |
| --- | --- | --- |
| Composition | `main.go:44-93` constructs one process-wide workspace and registers 13 Wails services | Simple and readable, but every exported method on concrete services becomes part of one broad bridge surface |
| Privileged backend | Workspace, big-file, Git, terminal, watcher, LLM, agent, DB, jobs, artifacts, settings, secrets, shell | All privileges originate from the same webview caller; there is no feature-scoped capability object |
| Workspace identity | `internal/workspace/workspace.go:200-252` stores one mutable root | Operations obtain the current root at different times; no immutable workspace/session handle accompanies work |
| UI orchestration | `frontend/src/state/store.ts` is a 1,823-line global Zustand store | Many domains share flags and fire asynchronous actions without request generations or ownership tokens |
| Events | Go emits string event names; `frontend/src/App.tsx:55-106` parses `unknown` payloads | Generated RPC types stop at the event boundary; payload compatibility is manual and partial |
| Long work | `internal/jobs`, private `internal/bigfile/jobManager`, synchronous bridge calls, and frontend busy flags | No single execution/lifecycle model covers cancellation, progress, logs, output commit, and workspace teardown |
| Persistence | Settings, secret map/keyring material, DB profiles, artifact registry, recovery manifests | Separate atomic files exist, but there is no shared schema version, migration registry, or cross-store transaction |
| File output | Workspace data tools, big-file plugins, editor saves, agent rollback, artifact registration | Safety and durability semantics differ by call path |

## Findings

### AR-001 — Advertised server mode would expose the desktop privilege graph as an unauthenticated remote control surface

**Severity:** Critical, conditional deployment blocker
**Confidence:** High for the design flaw; the checked-in target currently fails to compile, so this is not asserted to be an active deployed exploit

**Evidence**

- `Taskfile.yml:42-60` advertises `build:server`, `run:server`, `build:docker`, and `run:docker` as first-class tasks.
- `build/docker/Dockerfile.server` binds `WAILS_SERVER_HOST=0.0.0.0` and exposes the server transport.
- `main.go:79-93` registers the full desktop service graph without a server-specific allowlist: arbitrary workspace selection and filesystem operations, Git, PTY terminal, LLM, agent, DB, jobs, artifacts, settings, secret mutation, and native shell helpers.
- `internal/workspace/workspace.go:208-239` allows the caller to choose the active absolute directory; `internal/workspace/workspace.go:1845-1872` and `1915-1951` provide raw reads/writes, rename, and recursive delete within that chosen root.
- Inspection of pinned Wails alpha.79 server transport found no application authentication/body-limit layer in the default runtime route. The server websocket accepts the transport independently of the desktop webview trust assumption.
- Bounded builds of both Windows and `CGO_ENABLED=0` Linux `-tags "server production"` failed inside the pinned Wails dependency. The failure prevents the checked revision from producing this target, but it does not make the design safe if compilation is repaired.

**Impact**

Treating the bridge as an HTTP deployment changes the caller from a bundled, same-user webview into a network peer. A network caller could select a host directory as the workspace and then invoke the same methods used by the trusted UI. With terminal, agent, DB, secret-consuming services, and recursive filesystem mutations in the same graph, compromise is effectively equivalent to code execution as the Novera process user.

**Recommendation**

1. Remove or hard-disable all server/Docker tasks until there is a separately designed server product and a passing security test suite.
2. Do not reuse the desktop composition root. Create a server-specific service list containing only explicitly supported stateless operations.
3. Require authenticated sessions, origin/CSRF protection where relevant, authorization per method/resource, request and websocket size limits, rate limits, TLS termination, and a non-root/container sandbox.
4. Do not expose workspace selection, terminal, arbitrary file APIs, credential-consuming services, native dialogs, or agent mutations remotely.
5. Add a CI assertion that the desktop-only binary cannot start a listening network transport, and a separate threat model before any server target is restored.

### AR-002 — Concrete service binding accidentally publishes internal producer and raw-primitive methods

**Severity:** High
**Confidence:** High

**Evidence**

- `main.go:79-93` passes concrete implementation objects directly to `application.NewService`.
- `internal/jobs/jobs.go:74` labels `Start`, `Append`, and `Finish` as “producer API (not bound to the frontend)”, but they are exported methods on the bound `Service`.
- Generated bindings prove that the comment is false: `frontend/bindings/novera/internal/jobs/service.ts:24-25`, `46-47`, and `72-73` expose `Append`, `Finish`, and `Start` to JavaScript.
- The workspace binding similarly publishes `ReadRaw`, `WriteRaw`, `Delete`, `Open`, and other low-level primitives (`frontend/bindings/novera/internal/workspace/service.ts:85`, `149`, `209`, `219`, `279`, and `290`).
- The backend audit demonstrates concrete consequences in **BE-004**, including browser-side fabrication and mutation of job-ledger state.

**Impact**

Comments and intended usage do not define an authorization boundary. Any script executing in the application webview can call every generated method. Internal invariants such as “only trusted producers finish jobs” are therefore unenforced.

**Recommendation**

- Bind narrow facade types whose exported methods are the complete reviewed public contract. Keep producer objects unexported or behind package-level interfaces.
- Split `jobs.ReaderController` (list, detail, cancel, clear) from an unbound `jobs.Recorder` (start, append, finish).
- Replace raw filesystem primitives in the UI-facing facade with intent-level commands carrying validation, revisions, and lifecycle metadata.
- Add a generated-binding allowlist test that fails if a method appears unexpectedly. Review the output in every PR that changes an exported service method.

### AR-003 — Workspace identity is mutable global state rather than an immutable operation scope

**Severity:** High
**Confidence:** High

**Evidence**

- One `workspace.Service` is constructed at `main.go:45` and shared by Git, terminal, watcher, agent, artifacts, and the UI.
- `internal/workspace/workspace.go:200-205` returns the current mutable root; `Open` replaces it at `231-238`, and `Close` clears it at `243-251`.
- `frontend/src/state/store.ts:536-571` starts a multi-step switch, then launches Git and diagnostics work without attaching a workspace ID. `loadChildren` at `605-614` likewise commits whichever response returns.
- Events in `frontend/src/App.tsx:65-95` contain request IDs for some LLM traffic, but filesystem, jobs, artifacts, and menu traffic carry no workspace generation.
- Concrete stale-result and cross-root defects are detailed in **BE-027**, **FE-006**, **FE-009**, **FE-010**, **FE-012**, **FE-014**, and **FE-025**.

**Impact**

An operation started under workspace A can finish after workspace B becomes current. Depending on when a service calls `Root()`, this can produce stale UI state, use a terminal in the wrong directory, register an output in the wrong artifact registry, or apply an action to a different tree than the user approved.

**Recommendation**

Introduce an immutable `WorkspaceSession` with an opaque ID/generation and canonical root. Every workspace-scoped RPC request, job, event, approval, terminal, DB-derived artifact, and async UI result should carry that ID. The backend must reject a mutating continuation when the session is closed or no longer matches. The frontend must compare the response generation before committing it. Workspace switching should cancel and await owned operations before replacing the active session.

### AR-004 — Long-running work has multiple incompatible lifecycle systems

**Severity:** High
**Confidence:** High

**Evidence**

- `internal/jobs/jobs.go:1-5` describes the shared ledger and cancel hook.
- Workspace transforms call `runTracked`, but `internal/workspace/workspace.go:168-181` explicitly registers `nil` cancellation and executes synchronously.
- The large-file service creates a separate private `jobManager` (`internal/bigfile/fileservice.go:56-64`, `internal/bigfile/jobs.go:12-27`), uses different IDs, and emits `bigfile:job-*` events at `60-74`.
- `main.go:58` wires only workspace transforms into the shared Jobs/Artifacts services; `main.go:81` constructs the big-file service with neither dependency.
- The frontend listens for `jobs:changed` in `frontend/src/App.tsx:87-89` and cancels only shared jobs through `frontend/src/state/store.ts:1697-1710`; there are no listeners for `bigfile:job-start`, `bigfile:job-progress`, or `bigfile:job-end` and no UI call to `BigFile.CancelJob`.
- **FE-017A**, **BE-026**, **BE-030**, and **BE-032** document user-facing cancellation and retention consequences.

**Impact**

The Jobs panel is not an authoritative view of active work. Some work is visible but cannot be canceled, some is cancelable in the backend but invisible in the UI, and some bridge calls simply block. Workspace closure and application quit have no single list of operations to drain or abort.

**Recommendation**

Create one process-wide executor with workspace ownership and a uniform job state machine: queued, running, cancel-requested, succeeded, failed, canceled, and interrupted/recoverable. Each job should have an opaque UUID, typed progress, bounded logs, a context, input/output identities, and a commit/recovery phase. Make the Jobs service read/control facade the only bound surface. On workspace close, cancel and await jobs owned by that workspace; on restart, expose incomplete output/recovery state rather than silently losing it.

### AR-005 — Output-producing operations do not share a transactional file contract

**Severity:** High
**Confidence:** High

**Evidence**

- Workspace transforms independently open and create files throughout `internal/workspace/workspace.go`; `prepOutput` at `1002-1018` validates containment but not source/destination identity.
- Large-file plugins implement a different output path and recovery scheme under `internal/bigfile`.
- Agent mutations have a separate rollback implementation under `internal/agent`.
- Artifact registration is performed after a transform and is explicitly best effort: `internal/workspace/workspace.go:185-197` discards `CreateArtifact` errors.
- Settings, DB profiles, secrets, and artifacts each reinvent `path + ".tmp"` plus rename (`internal/settings/settings.go:277-303`, `internal/db/db.go:150-162`, `internal/secret/secret.go:68-80`, `internal/artifacts/artifacts.go:90-113`). None exposes a common durability contract or directory-sync behavior.
- Concrete failures include same-file truncation (**BE-003/FE-007**), non-atomic outputs (**BE-014/BE-030**), incomplete rollback (**BE-012/BE-013**), and recovery-manifest trust/durability gaps (**BE-033**).

**Impact**

Each feature makes its own decisions about aliases, temporary paths, permissions, fsync, rename semantics, partial cleanup, cancellation, backup caps, and lineage. A “successful” operation can therefore destroy its input, leave a partial output under the final name, or produce an unregistered result.

**Recommendation**

Build a common `outputtxn` abstraction that:

1. Resolves and records input identities, then rejects textual and hard-link aliases with `os.SameFile`.
2. Creates a unique same-directory temporary file with an explicit permission policy.
3. Streams under a context and enforces byte/record budgets.
4. Flushes, closes, and syncs data before an atomic replacement; syncs the directory where supported.
5. Commits artifact metadata only after content commit, or records a recoverable pending-metadata state.
6. Cleans partial output on failure/cancel and returns a structured result that distinguishes “not committed”, “committed”, and “commit uncertain”.

Use the same primitive for workspace transforms, big-file save-copy, agent-created artifacts, and JSON registries.

### AR-006 — There is no application-wide dirty-resource and shutdown coordinator

**Severity:** High
**Confidence:** High

**Evidence**

- Dirty state is largely represented on editor tabs in the global store; large-file staged edits live in backend sessions and are not part of the same model.
- `frontend/src/state/store.ts:536-558` clears tabs during workspace open, while `573-603` closes a workspace immediately and invokes backend cleanup fire-and-forget.
- Native window closing, File menu commands, workspace switching, tab closing, deletion, and “Save & Close” take different paths, as traced in **FE-002**, **FE-003**, and **FE-004**.
- Follow-tail owns another implicit data lifecycle and can replace staged state (**FE-005**).

**Impact**

The application cannot reliably answer “is it safe to close/switch/quit/delete this resource?” Text edits, table state, staged large-file changes, active transforms, terminals, and agent approvals have independent lifetimes. Data loss is consequently a systemic behavior, not a single missing confirmation dialog.

**Recommendation**

Define a resource lifecycle registry. Each open resource reports identity, workspace generation, dirty/staged state, active operations, save/discard capability, and a close hook. Route tab close, workspace close/switch, deletion, native close, and application quit through one asynchronous coordinator. It should freeze new work, gather blockers, present a single accessible decision surface, await saves/cancellations, and only then commit navigation or shutdown. Add end-to-end tests for every exit route and resource type.

### AR-007 — Persistence has no shared schema-version or cross-store consistency model

**Severity:** High
**Confidence:** High

**Evidence**

- Settings persist an unversioned `Settings` object (`internal/settings/settings.go:168-177`, `254-303`).
- Database profiles, the secret reference map/keyring, and workspace artifacts are separate unversioned stores (`internal/db/db.go:120-162`, `internal/secret/secret.go:51-80`, `internal/artifacts/artifacts.go:63-113`).
- Corruption behavior differs: settings and DB profiles rename corrupt files, the secret store renames then resets, while artifacts silently return an empty registry (`internal/artifacts/artifacts.go:83-86`).
- A DB profile and its secret are updated as separate actions; **BE-023** shows orphaning/inconsistency paths. **BE-024** and **BE-025** show additional key/corruption failure modes.
- No migration registry or format version was found for these durable structures.

**Impact**

Backward-incompatible field or secret-format changes have no deterministic migration path. A crash between related writes can leave a profile pointing to a missing secret or a secret with no profile. Users receive inconsistent recovery behavior depending on which JSON file failed.

**Recommendation**

Add explicit `schemaVersion` values, strict decode/validate steps, ordered idempotent migrations, durable backups, and user-visible recovery diagnostics. For DB profile plus secret changes, use an intent journal/two-phase sequence with rollback or store the relationship in a transactional database while keeping secret bytes in the OS store. Centralize corrupt-file naming and recovery reporting. Test upgrades from every released schema fixture and inject failures between each persistence step.

### AR-008 — Two data-transformation stacks encode overlapping formats and safety rules

**Severity:** High
**Confidence:** High

**Evidence**

- `internal/datatools` implements CSV and SQL dump inspection/transformation used by workspace methods.
- `internal/bigfile/plugins/csv` and `internal/bigfile/plugins/sql/*` independently implement dialect detection, parsing, SQL generation, extraction, splitting, reshaping, and cleanup.
- `README.md:36-41` presents the legacy and streaming suites as one product surface, but they have different job, artifact, cancellation, output, and escaping semantics.
- Backend findings **BE-015** through **BE-020** show correctness gaps that differ between those implementations, including dialect-specific SQL escaping and parser-boundary errors.

**Impact**

Fixes and tests applied to one stack do not automatically protect the other. Users receive different correctness and safety depending on which UI entry point happens to invoke a transform.

**Recommendation**

Define shared format contracts: dialect-aware identifier/value writers, streaming record interfaces, parser error policy, cancellation/progress hooks, source/output transaction semantics, and golden compatibility suites. Select one implementation per operation and deprecate the duplicate path. Until consolidation, document which engine each action uses and run the same adversarial fixtures against both.

### AR-009 — Event contracts are stringly typed and outside generated bridge compatibility checks

**Severity:** Medium
**Confidence:** High

**Evidence**

- Backend packages emit literal names such as `jobs:changed` and `bigfile:job-start`; no central event schema package spans Go and TypeScript.
- `frontend/src/App.tsx:55-95` accepts payloads as `unknown`, unwraps arrays manually, validates a few string fields, and casts the agent event to a store parameter type.
- Menu actions are converted from arbitrary event data with `String(...)` and cast to `AppMenuAction` at `93-95`.
- The unconsumed big-file job events in **AR-004** demonstrate that an event can exist in the backend without any compile-time indication that the frontend ignores it.

**Impact**

Renaming an event or changing a payload shape produces runtime-only failures, stale spinners, missed completion, or corrupted state. Partial validation protects against a crash but can silently discard necessary data.

**Recommendation**

Generate event name constants and discriminated payload codecs from one schema. Include `schemaVersion`, request/job ID, and workspace generation in every asynchronous event. Validate payloads at one adapter boundary, log rejected payloads with redaction, and expose typed subscriptions to components/stores. Add contract tests that enumerate every emitted event and assert a consumer or an explicit “telemetry-only” declaration.

### AR-010 — Frontend state is a single high-coupling store with shared busy flags and unowned async writes

**Severity:** Medium
**Confidence:** High

**Evidence**

- `frontend/src/state/store.ts` is 1,823 lines and owns workspace/tree, editor tabs, Git, chat/agent, DB, search, data tools, dialogs, jobs, artifacts, layout, settings, and global status.
- State initialization alone spans `frontend/src/state/store.ts:450-503`; workspace lifecycle logic at `505-630` mutates many domains together.
- A single `toolBusy` flag (`frontend/src/state/store.ts:476-479`) represents unrelated tools. **FE-043** documents the resulting coupling.
- Many actions start work with `void`, and response commits lack request tokens, as detailed in **FE-009**, **FE-014**, **FE-018**, **FE-020**, **FE-024**, **FE-025**, and **FE-027**.

**Impact**

The store makes unrelated feature changes risky and encourages last-response-wins behavior. It is difficult to cancel a domain, reset exactly one workspace, or test reducers without mocking the entire bridge.

**Recommendation**

Split state into domain slices/services with explicit ownership: workspace session, resources/editors, Git, assistant, DB, jobs/artifacts, and preferences. Model async operations as keyed resources (`idle/loading/success/error`, request token, generation), not booleans. Keep orchestration in a lifecycle layer that calls domain APIs; keep pure state transitions unit-testable. A split alone is not sufficient—request identity and cancellation must be part of the design.

### AR-011 — Backend facade files have become orchestration monoliths

**Severity:** Medium
**Confidence:** High

**Evidence**

- `internal/agent/agent.go` is 2,394 lines and combines conversation state, provider calls, tool selection/execution, approvals, mutation policy, audit logging, rollback, and command/network controls.
- `internal/workspace/workspace.go` is 2,084 lines and combines root lifecycle, traversal, search, diagnostics, small-file reads/writes, table browsing, legacy transforms, job/artifact wiring, and filesystem mutations.
- `internal/bigfile/document/file_document.go` is 1,490 lines and contains file access, caches/indexing, encoding/windowing, and related concurrency.
- These files directly contain many of the highest-severity state and resource bugs in the backend audit.

**Impact**

Large services conceal invariants and make it hard to test authorization, cancellation, and resource ownership independently. Exported-method growth also unintentionally expands the Wails surface.

**Recommendation**

Keep a small bound facade and extract pure/domain services behind unexported interfaces: `WorkspaceSessionManager`, `FileRepository`, `TableReader`, `TransformRunner`, `AgentRun`, `ApprovalBroker`, `CommandRunner`, and `CompletionClient`. Put policy at the facade boundary and pass immutable context inward. Use dependency injection for clocks, filesystem operations, event sinks, and transports so failure/race tests do not require a live Wails app.

### AR-012 — Artifact lineage is best-effort metadata rather than part of output correctness

**Severity:** Medium
**Confidence:** High

**Evidence**

- `internal/workspace/workspace.go:185-197` intentionally ignores artifact-registration failures.
- `internal/artifacts/artifacts.go:83-86` treats malformed JSON as an empty registry with no backup or error.
- Freshness is based only on modification times (`internal/artifacts/artifacts.go:116-140`); missing sources are skipped as “unverifiable” rather than surfaced as a distinct state.
- Big-file transform outputs are not wired to the shared artifact service at construction (`main.go:58`, `81`).

**Impact**

Output content and lineage can diverge. The UI may omit a valid output, silently forget the registry, or report an artifact as non-stale when a source is missing or timestamps are preserved. This undermines auditability for AI/data workflows.

**Recommendation**

Treat lineage as a commit participant. Store content hashes, source identities, tool/config version, workspace generation, created/updated timestamps, and a durable job ID. Represent `fresh`, `stale`, `source-missing`, `output-missing`, and `unverifiable` distinctly. If metadata commit fails after content commit, create a visible recoverable record. Wire every output-producing engine through the same registry.

### AR-013 — Production diagnostics and crash containment are too thin for a privileged desktop tool

**Severity:** Medium
**Confidence:** High

**Evidence**

- `main.go:35-40` only writes a terminal logger message before exit; production Windows builds use a GUI subsystem, so users may have no visible console.
- `frontend/src/main.tsx:1-8` mounts `<App />` directly without a React error boundary or a recovery surface.
- Event validation failures are silently ignored in `frontend/src/App.tsx:65-95`; several bridge cleanup failures are intentionally fire-and-forget (**FE-027**).
- Agent raw-response logging exists separately and is itself unsafe/unbounded (**BE-008**), rather than being part of a redacted application logging policy.

**Impact**

Startup, render, event-contract, and cleanup failures can appear as a blank window or inexplicable lost state. Conversely, the one detailed log path can retain sensitive model content. Support cannot reliably reconstruct a failure without risking overcollection.

**Recommendation**

Add a top-level error boundary, global rejected-promise/error capture, a user-visible startup failure dialog, and a bounded rotating local diagnostic log. Define structured event fields, redaction rules, and an opt-in support bundle that lists exactly what will be exported. Include build version, OS/runtime versions, workspace path hashes rather than raw paths where possible, active job states, and recent sanitized errors. Never log credentials or full model/tool bodies by default.

### AR-014 — The repository carries desktop, HTTP-server, Docker, and mobile surfaces without an explicit support policy

**Severity:** Medium
**Confidence:** High

**Evidence**

- Root `Taskfile.yml:3-9` includes Windows, macOS, Linux, iOS, and Android task sets.
- `Taskfile.yml:42-60` additionally exposes server and Docker deployment modes.
- CI exercises desktop builds on three OSes but does not build server, Docker, iOS, or Android (`.github/workflows/ci.yml:36-103`).
- Version metadata and templates exist across these targets despite differing maturity and, for server mode, current compile failure.

**Impact**

Task discoverability implies support. Untested templates drift, increase dependency/supply-chain surface, and can be enabled by a maintainer without noticing their security model differs from the desktop app.

**Recommendation**

Publish a target support matrix with `supported`, `experimental`, and `disabled` states. Remove disabled tasks from the default task list or make them fail with an explanatory message. Every supported target must have CI build/smoke coverage, version propagation, security assumptions, packaging ownership, and release criteria. Treat server mode as a separate product, not a build tag.

### AR-015 — Privileged actions lack a common capability/intent envelope

**Severity:** Medium
**Confidence:** High

**Evidence**

- The webview receives independent service methods for filesystem, Git, terminal, DB, LLM, agent, artifacts, secrets, and shell (`main.go:79-93`, `frontend/src/lib/services.ts:1-15`).
- Approval is implemented inside agent flows, while equivalent direct UI calls rely on component state and dialog routing rather than one backend intent policy.
- Actions generally pass primitive strings—paths, job IDs, SQL, commands, secret references—without a shared caller intent, workspace generation, resource revision, or approval token.
- Concrete confused-deputy and preview/approved-action mismatches appear in **BE-001**, **BE-034**, and **FE-022**.

**Impact**

Security policy is feature-local. A safeguard in agent mode does not automatically protect a direct bridge method, and approval can become detached from the exact operation later executed.

**Recommendation**

Represent sensitive work as immutable intents with a canonical digest: operation type, workspace session, exact resources, revisions, bounded preview, destination identity, provider/host, and expiration. The backend—not the UI—should determine whether an intent needs approval and execute only the digest that was approved. Issue short-lived capabilities scoped to one intent rather than trusting arbitrary caller-supplied secret refs or paths. Keep ordinary non-sensitive reads ergonomic, but use the same context envelope for identity and cancellation.

### AR-016 — The ported large-file subsystem retains a second product/configuration identity

**Severity:** Medium
**Confidence:** High

**Evidence**

- Canonical Novera settings use the OS configuration directory under `Novera/settings.json` (`internal/settings/settings.go:213-215`).
- The large-file port separately declares `QUARRY_HOME`, a `.quarry` directory, and its own settings path (`internal/bigfile/settings/settings.go:63-68`).
- Its large `AppSettings`/defaults model (`internal/bigfile/settings/settings.go:77-149`) is not the active source for most production `FileService` limits; those are hard-coded in `internal/bigfile/fileservice.go:23-45`.
- Active cache paths still use the Quarry directory (`internal/bigfile/cachepath/source.go:13-42`), a private logger writes `quarry.log` (`internal/bigfile/logger/logger.go:16-40`), and build info identifies `Quarry Editor` (`internal/bigfile/buildinfo/buildinfo.go:8-14`).
- The About/application build path does not expose that build-info object, leaving two partial identities rather than one supported configuration/diagnostic model.

**Impact**

Users and support can have state split between Novera and hidden Quarry locations. Settings that appear configurable in the port may be dead while runtime limits remain constants. Cache/log cleanup, migration, privacy documentation, and diagnostics become inconsistent.

**Recommendation**

Move all supported large-file limits, cache/log locations, and cleanup policy into canonical versioned Novera settings. Migrate or deliberately import existing `.quarry` data, then retire the old environment/directory name. Remove unused ported settings/build-info code after tests prove no consumer remains, and use one Novera build identity and redacted logging system.

## Positive architectural controls

- `main.go:44-58` is a clear composition root, making dependencies discoverable.
- Workspace package functions are already used to avoid binding some wiring methods (`internal/workspace/workspace.go:154-166`), showing awareness of bridge exposure even though the jobs object does not yet follow that pattern.
- Generated Wails bindings eliminate a large class of RPC name/signature drift.
- Workspace path handling centralizes canonicalization and containment, and symlink-aware checks are present before mutations.
- The large-file engine is decomposed below its facade into session, document, indexing, encoding, search, editing, in-place, and format-plugin packages.
- Settings sanitize embedded URL credentials and use restrictive file modes; several stores preserve corrupt data rather than blindly overwriting it.
- Jobs bound log retention and expose explicit lifecycle statuses; these are useful pieces to retain in the unified executor.
- Single-instance behavior reduces simultaneous-process corruption risk for per-user state.

## Recommended target architecture

### 1. Trusted desktop shell and explicit public facades

Keep the supported application local and desktop-only. Bind `WorkspaceUI`, `ResourceUI`, `JobsUI`, `AssistantUI`, `DatabaseUI`, and `PreferencesUI` facades, each with a reviewed allowlist. Internal recorders, secret readers, command runners, and raw storage helpers must not be exported on bound concrete types.

### 2. Workspace session as the root scope

Opening a workspace should create `{workspaceId, generation, canonicalRoot, context}`. Domain objects and jobs retain this immutable handle. Closing cancels the context and prevents further commits. Every response/event includes the ID; the frontend discards mismatches by construction.

### 3. Resource lifecycle registry

All editors and viewers implement a small lifecycle protocol: `status`, `save`, `discard`, `canClose`, and `close`. The coordinator owns close/switch/quit/delete and produces one accessible decision flow. Large-file staging becomes a first-class dirty resource rather than hidden backend session state.

### 4. Unified executor and output transaction

Every long action runs as a job with context, typed progress, bounded logs, ownership, and output transaction. The executor persists enough terminal/interrupted state to explain a restart. Output is invisible at its final path until commit, and artifact lineage is committed or marked recoverable alongside it.

### 5. Typed contracts and versioned persistence

Generate RPC and event DTOs from one contract source. Include request IDs and workspace generation. Version persisted schemas and build upgrade fixtures. Prefer structured error codes over matching strings such as the stale-file sentinel in `frontend/src/lib/services.ts:79-81`.

## Architecture acceptance criteria

The architecture remediation is complete when all of the following are true:

- A generated-binding snapshot contains only intentionally public methods; internal job producer and raw secret access cannot be called from JavaScript.
- No supported build opens a network listener. Any future server binary uses a separate composition root and passes authentication/authorization/abuse tests.
- Switching workspaces cancels and awaits all workspace-owned operations; injected late responses/events cannot change the new workspace.
- Text, table, and staged large-file resources all block close/switch/quit until saved or explicitly discarded.
- Every long transform appears in one Jobs panel, reports progress, can be canceled where technically safe, and cannot leave a partial final output.
- Same-file and hard-link source/output aliases are rejected before truncation across every transform engine.
- Settings, profiles, secrets, artifacts, and recovery records have schema versions, migrations, corruption recovery, and fault-injection tests.
- Every backend-emitted event has a generated payload type and an asserted consumer.
- A production crash/render failure creates a visible recovery path and a bounded redacted diagnostic record.
