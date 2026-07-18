# Frontend, UX, Accessibility, and Client-Side Reliability Review

## Executive summary

The frontend passes its present TypeScript, ESLint, unit-test, and production-build gates, and it contains several good defensive patterns. Those gates do not exercise the application workflows where the most serious failures occur. This review found **45 actionable findings**:

| Severity | Count |
|---|---:|
| Critical | 7 |
| High | 18 |
| Medium | 19 |
| Low | 1 |
| **Total** | **45** |

The highest-risk theme is data integrity. Normal user actions can lose edits: typing while a save is in flight, choosing “Save & Close” when the save fails, switching workspaces, quitting, closing a staged large-file edit, following a growing file, or selecting a tool output path equal to its input. The second major theme is asynchronous ownership. Most Wails calls have no workspace, request, or generation token, so late results can overwrite newer state or cross a workspace boundary. Accessibility also needs a deliberate pass: editor tabs, many result lists, splitters, several dialogs, and status updates are not operable or perceivable from a keyboard/screen reader.

Recommended release posture: treat FE-001 through FE-010 and FE-017 as release blockers for any build expected to protect user data. Treat the keyboard/dialog findings and the dependency audit as blockers for a public production release unless explicitly accepted.

## Scope and methodology

Reviewed:

- frontend/src, including the Zustand store, app shell, editors, large-file views, data tools, assistant, database, terminal, Git, jobs, dialogs, menus, styling, and tests.
- frontend/index.html, frontend/package.json, TypeScript/ESLint/Vitest configuration, and the Vite output.
- Relevant Go implementations where frontend safety depends on backend semantics, especially workspace writes, large-file sessions, and terminal startup.

Executed:

- npm run typecheck — passed.
- npm run lint — passed.
- npm test -- --reporter=verbose — passed: 1 test file, 5 tests.
- npm run build — passed, with very large Monaco/worker chunks described in FE-038.
- npm audit --json — completed after using the system certificate store; reported 5 vulnerabilities: 1 critical, 1 high, and 3 moderate. See FE-039.

“Confirmed” below means the behavior follows deterministically from the reviewed control flow. “Race” means the code permits the failure when operations resolve in an unfavorable order. “Gap” identifies a missing safeguard or validation even if no incident has yet been observed.

## Positive controls already present

These are worth preserving while refactoring:

- TypeScript strict mode is enabled, and the current codebase passes typecheck and lint.
- File opens deduplicate again at commit time in frontend/src/state/store.ts:639-646.
- External reload updates use a functional state update in frontend/src/state/store.ts:693-714, avoiding several ordinary save/close interleavings.
- Search has a stale-query check in frontend/src/state/store.ts:1647-1653, and database selection checks the active profile before committing in frontend/src/state/store.ts:1504-1511. These are good models for the generation guards needed elsewhere.
- CSV export neutralizes spreadsheet-formula prefixes in frontend/src/lib/grid.ts:3-14, and that behavior has unit coverage.
- Agent events are scoped by run ID, approval updates have an optimistic rollback path, and secrets are routed through the secret service.
- React rendering is used for assistant and workspace content; no application-owned dangerouslySetInnerHTML, innerHTML, eval, or Function-constructor sink was found.
- ConfirmModal and FileOpModal already implement part of the expected dialog semantics, including Escape and a focus loop.
- The terminal coalesces writes, filters events by terminal ID after subscription, and disposes listeners on cleanup.
- Large tables, trees, and big files use virtualization rather than rendering unbounded datasets.
- Backend revision checking refuses a normal stale-revision save instead of blindly overwriting an externally changed file.
- The splash stylesheet includes a prefers-reduced-motion rule, although the canvas animation does not honor it yet.

---

## Data integrity and application lifecycle

### FE-001 — A save can mark newer, unwritten edits as saved

- **Severity:** Critical
- **Classification:** Confirmed data-loss bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1001-1011; the same pattern exists in convertEncoding at frontend/src/state/store.ts:983-992.

The save call submits the tab content captured before awaiting Workspace.WriteFile. After the await, the state updater assigns savedContent from the tab’s current content, not the submitted snapshot. If the user types while the write is in flight, those new characters were never sent to disk, but content and savedContent become equal. The dirty indicator disappears. A later close no longer prompts and can discard the newer edits.

**Reproduction:** edit a file, invoke save, type again before the bridge write resolves, then close the tab. Adding latency to Workspace.WriteFile makes the defect deterministic.

**Recommendation:** capture submittedContent, submittedRevision, and a per-tab save generation before the await. On success, set savedContent only to submittedContent and update the revision only for that generation. Preserve newer content, which must remain dirty. Add a test with a deferred WriteFile promise and an edit between invocation and resolution. Apply the same contract to encoding conversion.

### FE-002 — “Save & Close” closes the tab even when saving failed

- **Severity:** Critical
- **Classification:** Confirmed data-loss bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:760-765 and 1015-1022.

The close-confirmation path awaits saveTab and then unconditionally calls closeTab. saveTab catches all errors, records an error message, and resolves void. Therefore a stale-revision conflict, permission failure, full disk, disconnected mount, or bridge error still causes the only in-memory copy to be closed.

**Reproduction:** make a dirty edit, externally modify the same file so the revision becomes stale, choose “Save & Close,” and observe that the save is rejected but the tab closes.

**Recommendation:** make saveTab return a typed result such as saved, conflict, failed, or superseded. Close only after a confirmed save of the intended snapshot. Keep the close dialog open on failure and offer Compare, Save Copy, Retry, and Cancel. Cover stale-revision and generic I/O failures in tests.

### FE-003 — Workspace switching, closing, quitting, native window closing, and deletion bypass a global unsaved-work coordinator

- **Severity:** Critical
- **Classification:** Confirmed safeguard gap with data-loss paths
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:536-602, 750-765, and 1610-1625; frontend/src/lib/menuActions.ts:89-94; main.go application lifecycle configuration.

openWorkspace and closeWorkspace replace the tab collection without first checking dirty tabs. The Quit menu calls Application.Quit directly. No beforeunload or Wails native-window close interception was found, while the application is configured to terminate with its last window. Deleting a file or directory removes matching open tabs after a generic disk-deletion confirmation; that dialog does not disclose that unsaved in-memory edits in the target subtree will also be destroyed.

The existing per-tab close modal is therefore not a complete safety boundary.

**Recommendation:** introduce one global “proposed destructive navigation” coordinator used by tab close, workspace switch/close, recent-workspace selection, quit, OS window close, file/directory deletion, and reload. It should enumerate all dirty resources, support Save All / Review / Discard / Cancel, await every save result, and abort the originating action on any failure. Native termination must be deferred until the coordinator resolves.

### FE-004 — Staged large-file edits are invisible to dirty-state and are destroyed on close

- **Severity:** Critical
- **Classification:** Confirmed data-loss bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:750-756; frontend/src/components/BigFileView.tsx:61-80, 220-223, and 414-433; internal/bigfile/fileservice.go:131-134.

Large-file staging lives inside BigFileView and its backend file session, outside the Zustand tab model. requestCloseTab explicitly excludes tooLarge tabs from its ordinary dirty check. When the component unmounts, it closes the large-file session. Closing a tab, switching workspace, quitting, or deleting its path can therefore destroy staged changes without any prompt.

**Recommendation:** represent staged large-file changes in shared resource state, including dirty flag, file ID, source revision, pending stage generation, and save status. Make the global coordinator from FE-003 query that state. Do not close a session with staged edits unless save/discard was explicitly resolved. Add integration tests for every close route.

### FE-005 — Follow-tail refresh silently discards staged large-file edits

- **Severity:** Critical
- **Classification:** Confirmed data-loss bug
- **Confidence:** High
- **Evidence:** frontend/src/components/BigFileView.tsx:436-440, 497-560, and 623-645; internal/bigfile/fileservice.go:150-153.

Entering edit mode does not disable Follow. When the source file grows, the follow effect invokes BigFile.RefreshFile. The backend contract explicitly states that RefreshFile discards staged edits. A user can enable Follow, edit and stage a region, then lose it merely because another process appended to the file.

**Recommendation:** make Follow and staged editing mutually exclusive. Entering edit mode should stop following; enabling Follow while dirty should require a discard confirmation. The backend should also reject refresh when a staging buffer exists unless an explicit discard flag is supplied, so safety does not rely on one UI.

### FE-006 — Workspace switching leaves old workspace state and pending operations alive

- **Severity:** Critical
- **Classification:** Confirmed cross-workspace state and destructive-action bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:536-558 and 957-967; frontend/src/App.tsx:109-135; frontend/src/components/ArtifactsView.tsx:27-29 and 64-69; frontend/src/components/Search.tsx:63-97.

openWorkspace resets only a subset of workspace-bound state. Search query/results, diagnostics, artifacts, pending tool/clean-dump state, pending reveal state, data-tool state, database/job UI state, and multiple modal targets can survive the switch. Ctrl+O is processed before the “plain field or modal” shortcut exclusion, so switching can occur while a modal is active.

This is not only stale presentation. For example, a Clean Dump modal targeting data.sql from workspace A can remain open; applying it after the switch sends a relative path to the backend’s current workspace B and can modify B/data.sql. ArtifactsView loads only on mount, so it can indefinitely display A’s artifacts while clicks resolve against B.

**Recommendation:** define a complete WorkspaceScopedState object and replace it atomically on workspace identity change. Close or cancel every workspace-scoped overlay before changing the backend root. Give pending operations immutable workspace IDs and reject execution when the current ID differs. Remount or explicitly reload views whose data source changes.

### FE-007 — Data tools allow source/destination aliasing and unconfirmed truncation

- **Severity:** Critical
- **Classification:** Confirmed destructive-file bug
- **Confidence:** High
- **Evidence:** frontend/src/components/ToolsModal.tsx:185-200; frontend/src/components/CleanDumpModal.tsx:98-105; frontend/src/state/store.ts:827-929 and 957-967; internal/workspace/workspace.go:720-743, 857-879, 905-954, and 971-990.

Several tools accept or derive an output path without proving it differs from the input and without checking whether it already exists. Backend implementations use os.Create, which truncates an existing destination. If CSV-to-SQL or dump cleaning uses the source as output, the input descriptor may be opened and then the same file truncated, producing empty/partial output and destroying the source. Fixed-name outputs such as extracted table SQL can also collide with the selected source or an unrelated existing file.

**Recommendation:** enforce canonical source != destination in both frontend and backend, including case-folding and symlink/alias considerations appropriate to the platform. Require an explicit overwrite confirmation that shows the resolved path. Write to a same-directory temporary file, fsync/close, and atomically replace only after successful completion. Prefer exclusive creation for new outputs. Add tests for identical, case-equivalent, relative-alias, and pre-existing paths.

### FE-008 — Large-file staging failures and overlapping stage calls can save an older edit

- **Severity:** High
- **Classification:** Confirmed error-handling bug and race
- **Confidence:** High
- **Evidence:** frontend/src/components/BigFileView.tsx:414-446 and 462-493.

stageNow catches StageEdit failures, sets a note, and resolves successfully. exitEdit then hides editing, while Save Patch and Save Copy continue as though the latest text was staged. They can save an earlier staging buffer, reset UI state, and discard the textarea’s latest value. Debounced and explicit stage calls are not serialized; both can use the same old original-length reference, and a late older completion can corrupt the next stage basis.

**Recommendation:** return a typed failure and never exit or save after a failed stage. Serialize stage mutations per file, attach a monotonically increasing edit generation, cancel obsolete debounce work, and await the latest generation before save. Keep the editor and text intact on every failure.

### FE-009 — Async results have no workspace epoch and can commit into a newer workspace

- **Severity:** High
- **Classification:** Cross-workspace race condition
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:536-680 and the analogous Git, diagnostics, index, artifacts, and jobs loaders.

Workspace changes have no transition lock or identity token. loadChildren, openFile, and most other async actions commit unconditionally after awaiting the Wails bridge. A request started under workspace A can complete after workspace B opens and inject A’s tab, tree children, search index, diagnostics, or status into B. Multiple openWorkspace calls can also resolve out of order, while the backend root is a single mutable service property.

**Recommendation:** generate an immutable workspaceEpoch before any switch. Capture it in every workspace-bound request and discard results unless it still matches at commit time. Abort/cancel old work where possible. Serialize backend root changes and disable repeated workspace-open actions while transitioning. Include A-slow/B-fast tests for each loader class.

### FE-010 — The integrated terminal remains rooted in the previous workspace

- **Severity:** High
- **Classification:** Confirmed cross-workspace behavior bug
- **Confidence:** High
- **Evidence:** frontend/src/App.tsx:181; frontend/src/state/store.ts:483-484, 536-558, and 1675; frontend/src/components/TerminalView.tsx:30-147; internal/terminal/terminal.go:65-76.

The terminal panel is deliberately kept mounted after first opening. A PTY is created once, using the workspace root at Term.Start time as its working directory. Directly opening another workspace does not close, restart, or relocate that terminal. The interface now presents workspace B while commands continue executing in A, making it easy to build, delete, or commit in the wrong project.

**Recommendation:** bind each terminal session to a displayed workspace ID and root. On switch, either close/recreate it after dirty-process confirmation, or retain it as a visibly labeled multi-workspace terminal. Do not silently issue cd because shells can reject or reinterpret it; a fresh PTY is the safest default.

### FE-011 — External-change protection does not refresh table and large-file views correctly

- **Severity:** High
- **Classification:** Confirmed stale-data/conflict bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:648-714; frontend/src/components/EditorPane.tsx:94-99.

Watcher synchronization includes only tabs whose kind is file. CSV/XLSX table tabs use kind table and are not watched or reloaded. Too-large and binary tabs can receive updated tab metadata, but EditorPane keeps the same BigFileView instance keyed by the unchanged path; its backend engine session is not refreshed or remounted. Users can inspect, transform, or export stale data after an external modification.

**Recommendation:** create view-aware resource watching. Table windows should invalidate and refetch with a new source revision. BigFileView should receive an explicit revision/version and coordinate RefreshFile, rejecting refresh when staging is dirty. Present compare/reload choices rather than silently replacing local work.

### FE-012 — Quick Open and derived indexes remain stale after filesystem mutations

- **Severity:** Medium
- **Classification:** Confirmed cache-invalidation bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1558-1632, 1677-1680, and 1794-1805.

Quick Open loads allFiles only when the array is empty. Create, rename, delete, and tool-generated-file actions refresh the tree or Git status but do not invalidate or update allFiles. New paths remain absent, while deleted and renamed paths remain selectable. Search results and diagnostics can likewise retain obsolete paths.

**Recommendation:** maintain one canonical workspace file index updated by watcher events and successful mutations, or invalidate all dependent indexes after every mutation. Remove stale palette entries immediately and refresh search/diagnostic path mappings on rename.

### FE-013 — “Keep mine” cannot resolve an external-edit conflict

- **Severity:** High
- **Classification:** Confirmed conflict-resolution UX bug
- **Confidence:** High
- **Evidence:** frontend/src/components/EditorPane.tsx:123-138; frontend/src/state/store.ts:1001-1022.

The conflict banner’s “Keep mine” action only clears staleOnDisk. The tab retains its stale revision, so the next normal save is rejected by the backend. “Reload” overwrites the dirty editor content. The advertised choice is therefore a dead end: one action does not permit saving and the other destroys the local version.

**Recommendation:** keep conflict state until a real resolution succeeds. Offer Compare/Merge, Save Copy, Reload Disk, and an explicit “Overwrite disk with mine” action that uses a backend force/expected-new-revision contract and a second confirmation. Preserve both versions throughout.

---

## Component concurrency, tools, and feature correctness

### FE-014 — Table window requests can overwrite newer filter/sort/delimiter results

- **Severity:** High
- **Classification:** Async race condition
- **Confidence:** High
- **Evidence:** frontend/src/components/TableView.tsx:59-123.

The initial effect has an alive guard, but loadWindow has no request generation or parameter key. A slow scroll request for an old filter, sort, sheet, or delimiter can resolve after a newer query and replace its rows. The shared inflight flag can also be cleared by an older request’s finally while a newer load is still active, allowing additional overlap.

**Recommendation:** derive a query key from every parsing/filter/sort parameter and increment a generation on change. Commit only when key and generation still match. Use AbortController where supported and track in-flight status per generation. Test old-slow/new-fast response ordering.

### FE-015 — Large-file navigation and mode changes commit out of order

- **Severity:** Medium
- **Classification:** Async race condition
- **Confidence:** High
- **Evidence:** frontend/src/components/BigFileView.tsx:167-180 and 366-385.

reset, find, goto, and mode-switch flows commit after awaits without a generation check. Rapid navigation or toggling can leave the mode label saying text while hex rows are displayed, or allow an old search result to replace the latest one. switchMode also schedules timeout work without retaining/clearing its handle.

**Recommendation:** use a view-query generation covering mode, offset, search, and file revision. Ignore obsolete results and clear timers on unmount or supersession.

### FE-016 — Closing a large-file component before OpenFile resolves leaks backend sessions

- **Severity:** Medium
- **Classification:** Confirmed resource-leak race
- **Confidence:** High
- **Evidence:** frontend/src/components/BigFileView.tsx:191-223; frontend/src/components/BigToolsModal.tsx:88-126.

Both components start OpenFile asynchronously and remember the returned ID only after resolution. If unmounted first, cleanup runs while the ID is empty. The late result is ignored or assigned after cleanup, and no later cleanup closes it. Repeated rapid opens/closes can accumulate engine handles and file descriptors.

**Recommendation:** if OpenFile resolves after the effect becomes inactive, immediately call CloseFile on the returned ID. Also make backend sessions cancellable/lease-based and expose active-session diagnostics for tests.

### FE-017 — Big Tools uses stale schema metadata and permits actions before its target is ready

- **Severity:** High
- **Classification:** Confirmed correctness and lifecycle bug
- **Confidence:** High
- **Evidence:** frontend/src/components/BigToolsModal.tsx:88-127, 166-188, 200-285, and 539-543.

Column/schema metadata is loaded for the initial delimiter/header settings. The user can then change those parsing settings without recomputing columns, yet transforms submit the changed settings with stale column indices. Changing the target does not fully reset profile, lint, result, and selected-table state; if a new SQL source has no tables, the old table can remain selected. Action buttons are disabled for busy but not consistently for loading or missing fileId. Backdrop/Close also remains active during work and can close the file session underneath an operation.

**Recommendation:** model the modal as explicit loading, ready, running, success, and error states. Reset all target-derived state on target change. Reload schema whenever delimiter/header/sheet changes and attach a schema revision to transforms. Disable actions and closing while an operation owns the session, or support explicit cancellation.

### FE-017A — Big-file transform progress and cancellation exist in the backend but are not integrated in the UI

- **Severity:** High
- **Classification:** Confirmed integration and operation-lifecycle gap
- **Confidence:** High
- **Evidence:** internal/bigfile/jobs.go:12-16 and 38-90; frontend/src/components/BigToolsModal.tsx:131-150; frontend/bindings/novera/internal/bigfile/fileservice.ts:20-25.

The backend wraps long streaming transforms in a single tracked job, emits bigfile:job-start, bigfile:job-progress, and bigfile:job-end, and exposes BigFile.CancelJob. No frontend source subscribes to those bigfile events or calls that cancellation method. BigToolsModal displays only a local undifferentiated busy state and final result. Users cannot tell whether a multi-gigabyte transform is progressing, what it is processing, or how to stop it, despite the backend already supporting safe cancellation and partial-output cleanup.

The modal’s backdrop and Close action also remain usable while busy. Closing can release the frontend’s large-file session while the transform RPC still owns work associated with that file, leaving the operation invisible and making its completion/error impossible to manage from the originating UI.

**Recommendation:** add a typed global big-file job state keyed by backend job ID, subscribe before starting transforms, show title/records/note/progress activity, and provide Cancel wired to BigFile.CancelJob. Keep the progress surface available if the tools modal closes, or prevent closing until cancel/completion. Reconcile job-end and RPC completion idempotently, validate event payloads, and test close/cancel/error ordering.

### FE-018 — Settings persistence is last-response-wins rather than last-edit-wins

- **Severity:** High
- **Classification:** Persistence race condition
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1379-1434; frontend/src/components/SettingsView.tsx:83-86.

Each setting action constructs and writes a whole Settings snapshot independently. Concurrent Save calls can finish out of order, so an older snapshot can become the durable file even when the in-memory UI shows newer values. Failures do not roll back or clearly identify which fields were not persisted. Base URL is saved on every keystroke, greatly increasing overlap and persisting partial URLs.

**Recommendation:** route all setting changes through one serialized, revisioned writer. Coalesce/debounce text input, write the latest complete snapshot atomically, and surface pending/saved/failed status. On failure, either restore the last durable snapshot or retain a prominent retry state.

### FE-019 — Chat and agent cancellation races with request creation

- **Severity:** High
- **Classification:** Async cancellation bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1139-1153, 1284-1287, and 1328-1346.

The cancel actions can call the backend only after Agent.Start or LLM.Send returns an ID. If the user cancels during that await, local state is cleared, but the late Start/Send result unconditionally installs its ID and resurrects a run the user believes was canceled. Events can then mutate the cleared conversation or conflict with a new request.

**Recommendation:** assign a client request generation before invoking the bridge. Cancellation marks that generation canceled immediately. If the backend ID arrives later, cancel it at once and never install it into active state. Ignore all events whose client generation is no longer active.

### FE-020 — Model discovery can apply results and defaults for an obsolete provider

- **Severity:** Medium
- **Classification:** Async state race
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1357-1375; frontend/src/components/AssistantPanel.tsx:273-281; frontend/src/components/SettingsView.tsx:68-81.

loadModels snapshots configuration, awaits a response, then commits models and may auto-save a model without verifying that provider/base URL still match. Provider changes do not consistently trigger a model refresh. A slow response from the prior provider can populate the selector or persist a model that is invalid for the new provider.

**Recommendation:** key model requests by provider, normalized base URL, and credential revision. Clear or mark the list stale immediately on provider changes, commit matching responses only, and validate the selected model against the current list before persistence.

### FE-021 — OpenAI/custom API keys cannot be removed while staying on that provider

- **Severity:** Medium
- **Classification:** Confirmed credential-lifecycle UX gap
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1437-1446; frontend/src/components/AssistantPanel.tsx:297-310; frontend/src/components/SettingsView.tsx:68-76 and 99-123.

The store supports an empty value as deletion, but both key inputs call save only when the field is nonempty. Clearing and blurring therefore does nothing. The only visible route that clears a key is switching to Ollama in Settings. Users cannot reliably revoke a saved credential while retaining the provider.

**Recommendation:** add an explicit Remove stored key action with confirmation and immediate secret-service deletion. Distinguish “stored key present” from the editable replacement field; never repopulate the secret into the DOM.

### FE-022 — Agent approval shows only a truncated mutation but approves the full one

- **Severity:** High
- **Classification:** Security/consent safeguard gap
- **Confidence:** High
- **Evidence:** frontend/src/components/AssistantPanel.tsx:108-144.

HTTP bodies and file writes are previewed only to 1,200 characters; edit before/after text is limited to 600. The Approve button authorizes the complete underlying operation, not the visible prefix. There is no hidden-byte count, digest, full diff, or “view all” route. A dangerous tail can therefore be outside what the user reviewed.

**Recommendation:** require a complete inspectable payload before approval. Use a scrollable full diff/body, explicit truncation indicator and byte count, downloadable/view-in-editor content for very large payloads, and a stable digest tied to the approved request. If the payload changes, invalidate approval.

### FE-023 — Assistant output is heuristically truncated and copied incompletely

- **Severity:** High
- **Classification:** Confirmed user-visible data-loss bug
- **Confidence:** High
- **Evidence:** frontend/src/components/AssistantPanel.tsx:10-47, 50-83, and 350-363.

visibleAssistantContent cuts content at internal-looking markers, a standalone “analysis” line, and several heuristic phrases. Legitimate prose or code containing those strings loses everything after the match. The Copy action uses the filtered version too, so the omitted content is not recoverable from the UI.

**Recommendation:** separate protocol channels structurally before data reaches the renderer. Never infer hidden protocol from free-form assistant text. Render and copy the complete user-visible content returned by the backend, and add regression cases containing every current trigger phrase as legitimate output.

### FE-024 — Database UI has cross-profile races and unsafe identifier construction

- **Severity:** Medium
- **Classification:** Confirmed correctness gaps and races
- **Confidence:** High
- **Evidence:** frontend/src/components/DatabasePanel.tsx:44-59, 67-110, 179, and 211-217.

toggleCols captures a connection ID but commits returned columns under only the table name without checking that the same connection remains active. Switching from A to B while A resolves can populate B with A’s same-named table metadata. Test-connection state is one ID; tests for different profiles can overlap and the first completion clears the second’s busy indicator. Save permits double submission for a new blank ID. openTable interpolates a raw metadata table name into SQL without database-specific identifier quoting, so spaces, reserved words, or quote characters fail.

Db.LoadError is also consumed without a rejection handler.

**Recommendation:** key every result and busy state by connection plus request generation. Disable/coalesce duplicate profile saves. Move table-query construction to the backend and quote identifiers according to the actual driver; do not treat metadata identifiers as SQL fragments. Surface all bridge failures.

### FE-025 — Git status responses can overwrite newer mutation results

- **Severity:** Medium
- **Classification:** Async race condition
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:1025-1096; frontend/src/components/SourceControl.tsx:173-209.

loadGitStatus can run while stage/unstage is in progress and commits unconditionally. A status request launched first can resolve after Stage and replace the newer returned status. Commit does not participate in the shared gitBusy state, and stage controls remain enabled while the component-local commit request runs.

**Recommendation:** serialize repository mutations, generation-guard status reads, and perform one authoritative refresh after each mutation. Use a single operation state shared by menu, component, and store callers.

### FE-026 — Terminal listeners are installed after the backend can already emit output or exit

- **Severity:** High
- **Classification:** Event-subscription race
- **Confidence:** High
- **Evidence:** frontend/src/components/TerminalView.tsx:76-100; internal/terminal/terminal.go:82-93 and 187-220.

The backend starts its read loop before Term.Start returns the terminal ID. The frontend subscribes to term:data and term:exit only after that promise resolves. An early shell prompt, error, or fast process exit can be emitted before listeners exist. A missed exit leaves the UI believing the PTY is still open, and subsequent Enter handling may not restart it.

**Recommendation:** subscribe before starting and route events through a pending-session token, or have Start return buffered initial output and current exit state atomically with the ID. Add a test backend that emits output and exit synchronously during startup.

### FE-027 — Watcher, terminal, and cleanup bridge failures are fire-and-forget

- **Severity:** Medium
- **Classification:** Confirmed error-observability gap
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:573-578 and 684-687; frontend/src/components/TerminalView.tsx:62-67 and 128-145.

Watcher.Watch, workspace close/reset calls, and terminal write/resize/close operations are invoked without awaited error handling. Rejections can become unhandled, external-change protection can silently stop, and users receive no indication that resize/input/cleanup failed.

**Recommendation:** centralize bridge invocation with structured error reporting and operation context. Await lifecycle-critical calls, catch best-effort cleanup failures explicitly, display watcher health, and add telemetry/logging without exposing secrets.

### FE-028 — Jobs can show the previous job’s log under the newly selected job

- **Severity:** Medium
- **Classification:** Confirmed stale-content bug
- **Confidence:** High
- **Evidence:** frontend/src/components/JobsView.tsx:22-29 and 43-56.

Changing openId does not clear the prior log. Until the new request resolves, job B’s panel displays job A’s lines. If the request fails, the old log remains indefinitely because the error is swallowed. Running durations also use Date.now without a timer, so the displayed duration freezes unless another job event happens.

**Recommendation:** clear or skeleton the log on selection, generation-guard the response, show failures, and tick running durations with a low-frequency timer disposed on unmount.

### FE-029 — File-only menu actions are enabled for synthetic and incompatible tabs

- **Severity:** Medium
- **Classification:** Confirmed action-eligibility bug
- **Confidence:** High
- **Evidence:** frontend/src/components/AppMenu.tsx:34-38; frontend/src/state/store.ts:770-823, 943-949, and 1745-1749.

canUseFile is derived from any activePath rather than the tab kind and a real workspace-relative path. “Save Artifact” can be enabled for diff, database, or table tabs even though the store rejects non-file tabs. A diff key such as diff:u:data.csv ends in .csv, so data tools can be enabled and pass a synthetic key to Workspace methods, yielding misleading backend errors.

**Recommendation:** derive commands from a typed active-resource capability object: realFile, editable, csv, dump, diff, database, largeFile, and so on. The store should validate the same capability at execution time.

### FE-030 — Rename and delete do not fully reconcile tab names, watcher paths, and synthetic tabs

- **Severity:** Medium
- **Classification:** Confirmed state-reconciliation bug
- **Confidence:** High
- **Evidence:** frontend/src/state/store.ts:161-174 and 1576-1625.

remapTab changes path but not the displayed tab name, so a directly renamed file can retain its old label. Rename does not resynchronize watchers, leaving the old path watched and the new path unprotected from external edits. Delete filters tabs by raw path, so synthetic diff keys such as diff:u:path and diff:s:path survive deletion. Watch synchronization is likewise not refreshed after deletion.

**Recommendation:** implement resource-aware rename/delete reducers that update path, label, URI/model identity, watchers, diagnostics, search results, Git/diff tabs, and file index in one transaction. Invoke them from both local mutations and watcher events.

---

## Accessibility, interaction, and responsive layout

### FE-031 — Editor tabs are not keyboard-operable and dirty tabs hide their only close control

- **Severity:** High
- **Classification:** Accessibility failure
- **Confidence:** High
- **Evidence:** frontend/src/components/EditorTabs.tsx:10-44; frontend/src/styles/global.css:233-237; frontend/src/App.tsx:109-149.

Tabs are clickable div elements without tablist/tab roles, tabIndex, aria-selected, or keyboard handlers. A keyboard user cannot focus and select them using the standard arrow/Home/End model. The close button for a dirty tab is display:none until pointer hover, so it cannot receive keyboard focus. No global Ctrl/Cmd+W tab-close shortcut is implemented.

**Recommendation:** implement the WAI-ARIA Tabs pattern with roving tabIndex, arrow/Home/End navigation, visible focus, aria-controls, Delete or Ctrl/Cmd+W support, and an always keyboard-reachable close button. Hiding via display:none must not depend solely on hover.

### FE-032 — Several dialogs lack focus containment, restoration, or safe destructive defaults

- **Severity:** High
- **Classification:** Accessibility and destructive-action safeguard gap
- **Confidence:** High
- **Evidence:** frontend/src/components/ConfirmModal.tsx:23-28 and 65; frontend/src/components/CloseTabModal.tsx:13-45; frontend/src/components/ToolsModal.tsx:36-57; frontend/src/components/CleanDumpModal.tsx:21-65; frontend/src/components/AboutModal.tsx:10-37; frontend/src/components/BigToolsModal.tsx:166-168 and 539-543.

CloseTabModal has no complete Tab loop or focus restoration. Tools, Clean Dump, Audit, About, and Big Tools have incomplete or absent initial focus, focus trap, role=dialog, aria-modal, Escape handling, and return-focus behavior. Background content is not made inert. ConfirmModal automatically focuses the primary action even when it is destructive, increasing accidental deletion risk.

**Recommendation:** use one reusable dialog primitive that provides semantic labeling, aria-modal, inert background, initial focus, Tab/Shift+Tab containment, Escape policy, and focus restoration. Focus Cancel for destructive confirmations. Do not let multiple window-level handlers process the same keystroke through stacked overlays.

### FE-033 — Search, Problems, Source Control, sortable headers, and context menus are pointer-only or semantically incomplete

- **Severity:** Medium
- **Classification:** Accessibility failure
- **Confidence:** High
- **Evidence:** frontend/src/components/Search.tsx:75-96; frontend/src/components/ProblemsView.tsx:38-51; frontend/src/components/SourceControl.tsx:93-117; frontend/src/components/VirtualGrid.tsx:120-133; frontend/src/components/FileContextMenu.tsx:29-48; frontend/src/components/FileTree.tsx:99-190.

Multiple result rows are clickable divs with no keyboard activation or list/option semantics. Sortable th elements respond only to click and do not expose aria-sort. The context menu lacks menu/menuitem roles, managed focus, arrow navigation, and reliable keyboard invocation. FileTree focuses the tree container but does not expose a roving focused item or aria-activedescendant, so selection changes may not be announced.

**Recommendation:** use native buttons/links where possible; otherwise provide focusability, Enter/Space behavior, names, state, and collection semantics. Implement keyboard sorting and aria-sort. Follow the ARIA tree and menu interaction models, including Shift+F10/context-menu key.

### FE-034 — Application menus, splitters, and the encoding popup omit standard keyboard behavior

- **Severity:** Medium
- **Classification:** Accessibility failure
- **Confidence:** High
- **Evidence:** frontend/src/components/AppMenu.tsx:119-180; frontend/src/components/Splitter.tsx:20-61; frontend/src/components/StatusBar.tsx:63-96.

Menus declare menu/menuitem roles but do not manage arrow, Home/End, typeahead, or focus return. Splitters are pointer-only divs without separator semantics, current values, focusability, or keyboard resize. The encoding popup has no menu/listbox semantics, focus management, or explicit Escape behavior.

**Recommendation:** implement the corresponding ARIA menu, separator, and listbox patterns. Splitters should support arrow-key increments, Home/End bounds, aria-orientation, aria-valuemin/max/now, and an accessible label.

### FE-035 — Async state is not announced and some controls remove focus indicators

- **Severity:** Medium
- **Classification:** Accessibility failure
- **Confidence:** High
- **Evidence:** frontend/src/components/StatusBar.tsx:40-60; frontend/src/styles/global.css:529-534, 643-647, 715, and 745.

Save/conflict/error and streaming/loading changes are primarily visual; the status bar is not a status/live region. Assistant streaming has only a visual cursor. Several text areas/inputs remove outline, and some do not replace it with an equally clear focus-visible treatment. Keyboard and screen-reader users can miss both focus location and operation results.

**Recommendation:** add targeted polite/assertive live regions for save results, errors, search counts, and completion states without making high-frequency streaming noisy. Define a consistent high-contrast :focus-visible ring and never remove outline unless a visible equivalent is guaranteed.

### FE-036 — Startup splash is a mouse-only skip target and ignores reduced motion in its canvas loop

- **Severity:** Medium
- **Classification:** Accessibility, lifecycle, and performance gap
- **Confidence:** High
- **Evidence:** frontend/src/components/BootSplash.tsx:53-71, 137-154, 238-276; frontend/src/styles/splash.css:380 onward; frontend/src/App.tsx:157-160.

The splash root is a clickable progressbar rather than a button, with no Enter/Space activation. beginLeave does not require initialization readiness, so a click can expose the application while startup work is incomplete. The overlay does not establish modal focus/inertness, allowing background global shortcuts. The CSS reduces CSS motion, but JavaScript still runs its canvas requestAnimationFrame loop. setProg runs on nearly every frame and rerenders the HUD about 60 times per second.

**Recommendation:** make Skip a real button enabled only when safe, or define a clear “continue startup in background” contract. Focus and isolate the overlay. Respect matchMedia(prefers-reduced-motion) in JavaScript and render a static frame. Throttle React progress state while allowing canvas animation to remain imperative.

### FE-037 — Persisted panel widths can collapse the editor at the supported minimum window size

- **Severity:** High
- **Classification:** Confirmed responsive-layout bug
- **Confidence:** High
- **Evidence:** frontend/src/App.tsx:161-167; frontend/src/state/store.ts:1791-1793; main.go:114-117.

The app grid combines the activity bar, sidebar, editor, assistant, and splitters. Sidebar and assistant widths are clamped independently to as much as 640 and 720 pixels, with no viewport-aware editor minimum. The native window permits a width of 900 pixels. At that supported size, open panels can consume more than the viewport and reduce the central editor to effectively zero width. No responsive media rules correct the main layout.

**Recommendation:** compute panel maxima from available width while reserving a usable editor minimum. Auto-collapse lower-priority panels below breakpoints, provide an explicit compact layout, and test every supported native-window size at 100%, 150%, and 200% display scaling.

---

## Performance, supply chain, testing, and hardening

### FE-038 — Monaco is eagerly loaded and produces multi-megabyte startup chunks

- **Severity:** Medium
- **Classification:** Performance gap
- **Confidence:** High
- **Evidence:** frontend/src/main.tsx:3; frontend/src/lib/monaco.ts:4-11; production Vite build output.

The production build succeeds, but Monaco is imported before App and imported as a complete namespace. Reported minified output includes approximately:

- monaco JavaScript: 3,326.72 kB, 852.49 kB gzip.
- TypeScript worker: 6,013.96 kB.
- CSS worker: 1,009 kB.
- HTML worker: 668 kB.
- JSON worker: 362 kB.
- editor worker: 230 kB.
- main application JavaScript: 368.61 kB, 109.70 kB gzip.

In a desktop bundle, network latency may be absent, but parse/compile time, memory, startup I/O, packaging size, and worker initialization remain real. This also competes with the already animation-heavy splash.

**Recommendation:** lazy-load Monaco only when a text editor is first needed, import/register only required languages and workers, and measure cold-start, time-to-interactive, memory, and packaged size before/after. Keep a lightweight viewer path for binary/table/large-file tabs.

### FE-039 — Development dependencies contain one critical, one high, and three moderate vulnerabilities

- **Severity:** High
- **Classification:** Supply-chain and development-environment risk
- **Confidence:** High
- **Evidence:** frontend/package.json:37-38 and the current npm audit result.

npm audit reports five vulnerable packages in the installed dependency graph. Directly relevant roots are Vite 5 and Vitest 2. The critical Vitest advisory GHSA-5xrq-8626-4rwp concerns file read/execute exposure when the Vitest UI/server is listening. Vite/esbuild advisories include development-server request and filesystem exposure classes. npm’s proposed fixed versions require major upgrades (reported as Vitest 4.1.10 and Vite 8.1.5 at review time).

These packages are development tooling and are not evidence of a direct exploit in the packaged Wails runtime. Risk becomes material on developer/CI machines when preview, test UI, or dev servers are exposed beyond a trusted local interface.

**Recommendation:** plan and test the major upgrades, regenerate the lockfile, rerun all gates, and configure dev/test servers to bind only to loopback unless explicitly needed. Add dependency auditing to CI with an agreed policy that distinguishes shipped runtime from development-only exposure.

### FE-040 — Five pure helper tests do not cover any critical frontend workflow

- **Severity:** High
- **Classification:** Test-strategy gap
- **Confidence:** High
- **Evidence:** frontend/vitest.config.ts:3-9; frontend/src/lib/grid.test.ts; frontend/package.json:28-38.

Vitest is configured for node and only src/**/*.test.ts. The suite contains one pure grid helper file with five tests. There is no DOM environment or component testing dependency and no store/Wails mock integration suite. None of the data-loss, lifecycle, race, focus, or event-ordering paths in this report is protected.

**Recommendation:** add a DOM component stack and a deterministic bridge mock with deferred promises/events. Prioritize:

1. edit-during-save and failed Save & Close;
2. workspace switch/quit with dirty normal and large-file tabs;
3. old-workspace async completion after a new workspace opens;
4. BigFile staging/follow/refresh failures;
5. table old-slow/new-fast loads;
6. chat/agent cancel-before-ID;
7. terminal event-before-Start-return;
8. big-file transform progress, cancel, and modal-close ordering;
9. dialog focus trap/return and tab keyboard navigation;
10. source/destination tool aliasing and overwrite confirmation.

Add a small packaged-app E2E layer for native close/quit and PTY behavior, which unit tests cannot faithfully cover.

### FE-041 — Event payloads are cast after minimal checks, and compiler/lint rules permit broad any use

- **Severity:** Medium
- **Classification:** Runtime robustness gap
- **Confidence:** High
- **Evidence:** frontend/src/App.tsx:77-81; frontend/src/components/AssistantPanel.tsx:370-391; frontend/src/components/TerminalView.tsx:14-25 and 90-99; frontend/tsconfig.json:18-21; frontend/eslint.config.js:18.

Agent events validate little beyond runId/type before a double cast. A malformed plan can reach filter/map and throw. Terminal payloads check only for id before casting and passing data to atob, which can also throw. strict is enabled, but noImplicitAny is explicitly false and the ESLint no-explicit-any rule is disabled, weakening the boundary where generated/runtime payloads most need validation.

**Recommendation:** validate every bridge/event payload with small type guards or a schema library at the boundary, including arrays, enum values, strings, and base64. Catch decoding failures. Re-enable noImplicitAny, gradually tighten explicit-any usage, and isolate generated bindings through typed adapters rather than weakening the entire frontend.

### FE-042 — Clipboard actions fail silently

- **Severity:** Low
- **Classification:** Confirmed UX/error-handling gap
- **Confidence:** High
- **Evidence:** frontend/src/components/TableView.tsx:137-145; frontend/src/components/DatabasePanel.tsx:44-46; frontend/src/components/AssistantPanel.tsx:356-363.

Clipboard writes are not awaited and failures/unsupported environments produce no user feedback. A Copy action can appear to succeed while leaving the clipboard unchanged.

**Recommendation:** await writes, show a brief success state, report permission/availability failures, and provide a selection fallback where practical.

### FE-043 — A single global toolBusy boolean is not safe for overlapping tool actions

- **Severity:** Medium
- **Classification:** Async operation-state race
- **Confidence:** Medium
- **Evidence:** frontend/src/state/store.ts tool and data-tool action implementations around 770-967.

Multiple tool actions share one boolean and common result/error slots. The UI often discourages overlap, but store methods do not enforce mutual exclusion and commands can also arrive from native menus/shortcuts. If two calls overlap, the first finally can set toolBusy false while the second still runs, and either result can overwrite the other’s output.

**Recommendation:** enforce single-flight execution in the store or track operations by ID/type. Only the owning operation may clear its busy state or result. Prefer explicit cancellation and per-modal operation state over a global boolean.

### FE-044 — CSP comments overstate production network isolation

- **Severity:** Medium
- **Classification:** Defense-in-depth and configuration gap
- **Confidence:** High
- **Evidence:** frontend/index.html:5-14.

The comment says remote connect loads are blocked, but connect-src allows scheme-wide ws: and wss:, not only a loopback Vite HMR socket. script-src permits unsafe-inline and unsafe-eval, weakening script-injection containment, and frame-ancestors is absent. The same static policy is emitted for development and production even though the comment attributes the websocket allowance to development HMR.

No application-owned raw-HTML or dynamic-code injection sink was found, so this should not be represented as a presently demonstrated exploit. It is a mismatch between the stated defense and the actual policy, and it increases impact if a future dependency or rendering sink is compromised.

**Recommendation:** emit separate development and production CSPs. In production, remove ws:/wss: unless a concrete feature requires them, constrain development HMR to exact local origins, add frame-ancestors 'none', and investigate replacing unsafe-inline/unsafe-eval with Monaco-compatible hashes/nonces or worker configuration. Add a build assertion/test that captures the effective production policy.

---

## Prioritized remediation plan

### P0 — Protect data before expanding features

1. Implement snapshot-correct, typed-result saves and make all close flows await confirmed persistence (FE-001, FE-002).
2. Build one global unsaved-resource coordinator covering normal editors, large-file staging, workspace switch, delete, quit, and native close (FE-003, FE-004).
3. Prevent Follow/Refresh and stage/save races from discarding large-file edits (FE-005, FE-008).
4. Enforce source/destination separation, confirmed overwrite, and atomic tool output in frontend and backend (FE-007).
5. Atomically reset workspace-scoped state and introduce workspace/request epochs (FE-006, FE-009).
6. Rebind or recreate terminal sessions on workspace change (FE-010).

### P1 — Make async feature state trustworthy

1. Repair external-change handling and conflict resolution (FE-011, FE-013).
2. Add request generations to table, big-file, database, Git, model, terminal, and job flows (FE-014, FE-015, FE-019, FE-020, FE-024, FE-025, FE-026, FE-028).
3. Redesign Big Tools state, large-file session ownership, and transform progress/cancellation (FE-016, FE-017, FE-017A).
4. Serialize settings persistence and provide credential deletion (FE-018, FE-021).
5. Make approval previews complete and remove heuristic assistant truncation (FE-022, FE-023).
6. Reconcile all path-derived caches and command capabilities on file operations (FE-012, FE-029, FE-030).

### P1 — Accessibility and supported-window usability

1. Implement keyboard-operable editor tabs and a shared accessible dialog primitive (FE-031, FE-032).
2. Convert pointer-only controls to native/ARIA patterns; add keyboard splitters and menu behavior (FE-033, FE-034).
3. Restore focus-visible styling, announce async state, honor reduced motion, and fix the minimum-width layout (FE-035, FE-036, FE-037).

### P2 — Engineering controls

1. Lazy-load and trim Monaco, then establish startup budgets (FE-038).
2. Upgrade/audit development tooling and harden the release CSP (FE-039, FE-044).
3. Build deterministic async, component, accessibility, and packaged-app tests before refactoring these workflows (FE-040).
4. Validate bridge payloads, centralize error reporting, and use operation-scoped busy/result state (FE-027, FE-041, FE-043).
5. Finish smaller feedback improvements such as clipboard status (FE-042).

## Suggested release acceptance criteria

- No destructive navigation can proceed while any normal or large-file resource is dirty unless the user explicitly resolves every item.
- A save is associated with an immutable content snapshot and cannot clear dirtiness for later edits.
- Every workspace-bound async result is rejected after its workspace epoch changes.
- Every tool output uses a distinct, resolved destination and an atomic commit; pre-existing output requires explicit consent.
- Switching workspaces cannot leave a terminal, modal, artifact, search result, watcher, or pending operation bound to the old root without a visible label and explicit design.
- Core workflows have old-slow/new-fast and failure-injection tests, not only happy-path tests.
- All primary application actions are operable with keyboard alone, dialogs retain/restore focus, and critical state changes are announced.
- The editor remains usable at the declared 900 x 600 minimum window size and at common display-scaling settings.
- Production dependencies pass the agreed audit policy, and the effective production CSP matches its documentation.
