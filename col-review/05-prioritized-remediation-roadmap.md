# Prioritized Remediation Roadmap

## Release recommendation

**Do not treat the reviewed revision as ready for a public data-safe release.** The frontend and most backend checks pass, but the review found reproducible or strongly evidenced paths to silent source truncation, credential misuse, unauthorized recovery replay, lost edits, cross-workspace stale writes, approval/operation mismatch, and unsafe remote deployment if server mode is repaired. Delivery automation also cannot prove that a tag’s version or validation state matches its published binaries.

This roadmap orders work by risk and dependency, not by implementation convenience. IDs refer to the detailed evidence in:

- [01-architecture-and-cross-cutting-review.md](01-architecture-and-cross-cutting-review.md)
- [02-backend-security-and-reliability.md](02-backend-security-and-reliability.md)
- [03-frontend-ux-and-accessibility.md](03-frontend-ux-and-accessibility.md)
- [04-testing-build-release-and-maintainability.md](04-testing-build-release-and-maintainability.md)

## Priority definitions

- **P0 — release blocker:** realistic credential compromise, remote compromise at an advertised boundary, silent data loss, approval bypass/mismatch, or a delivery gate that can publish an unvalidated/misidentified build.
- **P1 — required before broad beta:** serious resource exhaustion, cancellation/concurrency, database security, persistence/recovery, accessibility, or operational defects likely to harm real users.
- **P2 — hardening and maintainability:** architecture consolidation, deeper performance/reliability evidence, governance, packaging polish, and defense in depth.

“Fixed” means the root invariant is implemented across every relevant path and an adversarial regression test passes. Closing only the example path is not enough.

## Wave 0 — Contain exposure and preserve evidence

These actions are low-dependency containment and should precede broad refactoring.

| Work item | Priority | Findings | Required outcome |
| --- | --- | --- | --- |
| Disable server/Docker commands | P0 | AR-001, QR-001, QR-002 | Tasks fail with an explicit “unsupported/security design pending” message; no supported binary listens on a network interface |
| Freeze public release automation | P0 | QR-003, QR-004, QR-005 | Tag workflow cannot publish until the validation/version gates in Wave 4 exist |
| Warn users about destructive transform aliasing | P0 containment | BE-003, FE-007 | UI temporarily rejects identical normalized source/output text while the backend identity-safe transaction is built; warning is not considered the final fix |
| Stop unconditional raw agent logging | P0 | BE-008 | Raw bodies are off by default immediately; existing logs are documented and a safe deletion path is provided |
| Preserve failing/corrupt artifacts | P0/P1 | BE-024, BE-025, AR-007 | Do not overwrite corrupt key/profile/artifact/recovery files; surface their paths and recovery instructions |

Do not “fix” the Defender-blocked jobs tests by retaining the CI exclusion. Preserve the failing executable hash/log, submit/diagnose it, and establish an alternate required Windows runner before changing the package.

## Wave 1 — Restore hard security and data-integrity invariants

### 1A. Unify safe file output

**Primary findings:** BE-003, BE-012, BE-013, BE-014, BE-030, BE-033, BE-039, BE-040, BE-041; FE-007; AR-005.

Build a single backend `OutputTransaction` used by workspace data tools, big-file save-copy/transform output, agent artifact creation, and JSON registries.

Required semantics:

1. Capture the input identity before opening an output.
2. Resolve the proposed destination and reject the same normalized path, symlink target, or hard-link identity using file metadata/`os.SameFile` where possible.
3. Create a unique temporary file in the destination directory; never use a predictable shared `.tmp` name.
4. Stream with a cancellable context and explicit record/byte budgets.
5. Treat short reads, premature EOF, parser errors, close errors, flush errors, and cancellation as failure.
6. Sync/close the temporary file, preserve/choose explicit permissions, atomically replace only after success, and sync the directory where supported.
7. Remove partial temporary output on failure; if final commit state is uncertain, report it explicitly rather than claiming success.
8. Register lineage after content commit or write a durable recoverable metadata intent.

Regression matrix:

- identical source/output strings;
- path aliases (`.`/`..`, case variants where applicable, symlink, hard link);
- existing and new destination;
- disk full/permission loss at write, flush, close, rename, and metadata commit;
- cancel at each phase;
- giant single record/line and malformed/truncated input;
- Windows replace/antivirus interference and Unix permission preservation.

### 1B. Authenticate and scope recovery

**Primary findings:** BE-002, BE-033, FE-004, AR-005.

- Do not automatically apply any adjacent `.qrp` file merely because its name matches.
- Bind the recovery record to an exact canonical file identity, pre-image hash/size/revision, application format version, and authenticated integrity tag or equivalent trusted journal.
- Validate every target offset/range and impose total recovery size limits before allocation/read.
- Present a recovery choice with exact file identity and backup path when automatic recovery cannot be proven safe.
- Make replay idempotent and preserve the journal if the commit result is ambiguous.

Tests must cover attacker-created sidecars, copied sidecars from another file, truncated manifests, changed source files, path substitution, interrupted replay, and replay twice.

### 1C. Replace caller-selected secret references with owned capabilities

**Primary findings:** BE-001, BE-023, BE-024, AR-007, AR-015.

- A DB profile ID must resolve only to the secret reference owned by that profile. The caller cannot submit an arbitrary existing ref.
- Secret updates and profile updates need an intent journal/transaction with rollback and orphan cleanup.
- Secret bytes remain backend-only; public DTOs expose presence/state, never a reusable capability reference with cross-feature meaning.
- Bind secret access to purpose (`db-profile:<id>`, `llm-provider:<id>`), not a global caller-chosen string namespace.

Adversarial tests: attempt to attach an LLM secret to a DB profile, reuse another profile’s ref, crash between secret/profile writes, delete a profile during save, corrupt the key material, and retry idempotently.

### 1D. Make approval authorize the exact operation

**Primary findings:** BE-034, FE-022, AR-015.

The backend must create a canonical immutable intent containing full command/query/path/content/destination/provider details, workspace generation, resource revisions, limits, and expiry. The UI renders the complete or safely paginated intent and approves its digest. Execution consumes that one-time approval only if the digest and context still match. Never approve a clipped preview that authorizes a longer hidden payload.

Regression tests should mutate each field after preview, use oversized content, switch workspaces, let approval expire, replay a token, and attempt an artifact-creation route that previously skipped approval.

### 1E. Harden agent/LLM network and response boundaries

**Primary findings:** BE-005, BE-006, BE-008, BE-009, BE-036, BE-042; FE-019, FE-020, FE-023.

- Forbid credential-bearing calls over non-loopback plaintext HTTP. Custom remote providers require verified TLS; certificate failures must not downgrade.
- Resolve and pin the approved destination per connection; revalidate every redirect and protect against DNS rebinding/private/link-local/metadata ranges.
- Apply header/body limits before buffering; stream completions with total byte/token caps and strict timeout bounds.
- Treat malformed/truncated protocol frames and missing terminal markers as errors.
- Redact structured metadata; never log API keys, authorization headers, full prompts, responses, or tool bodies by default.
- Allocate the run/request ID before exposing cancellation; make cancellation idempotent and tied to the exact run.

### Wave 1 release gate

Wave 1 is complete only when every above regression runs in CI and a destructive fault-injection suite cannot damage the source or execute/consume a different intent/secret than the one selected and approved.

## Wave 2 — Correct lifecycle, concurrency, and resource ownership

### 2A. Introduce workspace generations and operation ownership

**Primary findings:** BE-027, FE-006, FE-009, FE-010, FE-012, FE-014, FE-015, FE-018, FE-020, FE-024, FE-025; AR-003.

Opening a workspace must return an opaque session/generation. Every workspace-scoped request, response, event, job, approval, terminal, table cursor, big-file session, artifact, Git result, and agent run carries it. The backend rejects commits after the session context is canceled; the frontend commits a response only when its generation/request token remains current.

Sequence for switching:

1. Ask the lifecycle coordinator whether all resources can close.
2. Freeze creation of new workspace-owned work.
3. Cancel active requests/jobs/terminal/agent and await bounded cleanup.
4. Close watchers, table cursors, big-file sessions, and DB/resource views for that generation.
5. Activate the new backend session.
6. Reset domain slices atomically, then start new generation work.

Test delayed A responses after B opens, rapid A→B→C switches, rejection/cancellation races, old filesystem events, and terminal output after teardown.

### 2B. Build one dirty-resource lifecycle coordinator

**Primary findings:** FE-001 through FE-005, FE-011, FE-013, FE-029, FE-030; AR-006.

All text editors, table viewers with editable state, big-file staged sessions, and other mutable resources implement identity, revision, dirty/staged state, save/discard, and asynchronous close. Tab close, Save & Close, workspace switch/close, delete/rename, native window close, and app quit invoke one coordinator.

Required save invariant: a save response may mark only the exact captured edit revision clean. Edits typed while the save is in flight remain dirty. A failed save never closes the resource. “Keep mine” must refresh the expected disk revision or perform an explicitly confirmed force-save flow.

Required large-file invariant: backend staging is visible in UI dirty state; follow-tail/navigation cannot discard it; close/quit either saves, discards after confirmation, or remains open.

### 2C. Merge all long work into one executor/jobs model

**Primary findings:** BE-010, BE-026, BE-030, BE-032; FE-017A, FE-019, FE-028, FE-043; AR-004.

- Replace private big-file jobs and workspace nil-cancel jobs with one executor.
- Job IDs are globally opaque and never reused within durable history.
- Active jobs cannot be evicted; completed history has separate bounded retention.
- State transitions are atomic and monotonic; `cancel-requested` is distinct from `canceled`.
- Each job owns context, workspace generation, input/output identities, typed progress, bounded logs, and artifact commit status.
- Frontend busy state is per operation/resource, not one global boolean.

Agent cancellation must wait for the old run to terminate before allowing a new active run. Command cancellation must terminate the full process tree and classify nonzero exit/timeout/cancel accurately.

### 2D. Fix session/cache/watcher concurrency

**Primary findings:** BE-028, BE-029, BE-037; FE-015, FE-016, FE-026, FE-027.

- Big-file registry operations require explicit ownership/reference lifetime; a late `OpenFile` completion that no longer has a component owner closes its session.
- Cache keys include file identity/revision; refresh invalidates SQL/table/index caches.
- Watcher goroutines receive an immutable watcher pointer, have a wait group, propagate errors, and are joined before state is cleared.
- Terminal/event listeners are established before output can be emitted, or the backend provides a sequence/replay buffer.

Run targeted `-race` loops for open/close/refresh/cancel/shutdown and retain failing seeds/traces.

## Wave 3 — Bound resource use and correct format/database semantics

### 3A. Enforce budgets before allocation/read

**Primary findings:** BE-008, BE-009, BE-011, BE-012, BE-020, BE-021, BE-031, BE-039, BE-040; FE-023.

Use centralized, clamped configuration for maximum request/response bytes, command bytes/lines, log bytes, DB rows/cells/bytes, CSV record bytes, SQL statement/line bytes, rollback bytes, search matches, and UI copy/export sizes. Apply `LimitReader`/streaming/counter checks before allocation or full reads. Return structured “truncated” metadata; never represent incomplete output as complete success.

### 3B. Replace heuristic SQL safety and fix TLS identity

**Primary findings:** BE-007, BE-016, BE-018 through BE-022, BE-035; FE-024.

- Use a dialect-aware parser/AST for read-only validation; quoted identifiers remain tokens and dangerous function calls are recognized.
- Enforce least-privileged DB users, read-only transactions, server statement timeouts, row/byte caps, and cancellation as independent defenses.
- Default PostgreSQL to hostname-verifying TLS (`verify-full` semantics) and MySQL to verified TLS; provide explicit CA/client certificate configuration. Unverified encryption is an opt-in with a prominent warning.
- Centralize dialect-aware identifier/value output. For MySQL, handle backslash mode correctly or use safe literal encodings; validate generated dumps by importing into real engines.
- Stop constructing identifiers in the frontend; backend accepts structured table/schema selection and quotes by dialect.

### 3C. Make parsers fail closed and preserve record boundaries

**Primary findings:** BE-015, BE-017, BE-018, BE-019, BE-020, BE-039, BE-040, BE-042.

Parser errors, truncated COPY sections, discontinuous table segments, malformed model frames, and encoding-boundary splits must return explicit partial/error status. Regex replacement must not mutate comments/strings unless the operation promises that scope, and replacement `$`/backslash semantics must be literal when advertised. Build golden adversarial corpora shared by both legacy and large-file transformation stacks.

## Wave 4 — Make the release evidence trustworthy

### 4A. Repair the test and CI foundation

**Primary findings:** QR-005, QR-006, QR-009 through QR-017, QR-031, QR-035; FE-039 through FE-041.

Required pull-request workflow:

- explicit project Go packages, immediate failure propagation, and a required Windows jobs test path;
- `go mod tidy -diff`, generated binding/event schema diff, `go vet`, scoped static analysis, unit tests, targeted race tests, and bounded `govulncheck`;
- `npm ci`, typecheck, lint, unit/component/accessibility/E2E tests, production build, production-CSP and bundle-budget assertions, and production dependency audit;
- secret/dependency/code scanning with triage policies;
- clean-tree assertion after all generation/build commands.

The release workflow must call the same reusable validation for the immutable tag commit rather than assume a branch workflow passed.

### 4B. Establish one version and artifact identity

**Primary findings:** QR-003, QR-027, QR-034.

Derive semantic version/channel from the validated tag; inject version, commit, and reproducible timestamp into one build-info package and all platform metadata. The application About/diagnostics displays it. CI extracts PE resources, plist/package fields, archive name, runtime version, architecture, and embedded frontend hash and compares them with the tag.

### 4C. Lock and attest the supply chain

**Primary findings:** QR-008, QR-018 through QR-022, QR-034.

- Pin Actions and images by immutable digest/SHA.
- Verify every compiler/SDK download with reviewed hashes/signatures.
- Use lockfile-strict installs and an update bot with grouped reviewed PRs.
- Build from clean exported source and record toolchain/container digests.
- Generate SBOM and provenance, then sign/notarize artifacts and verify signatures before publishing.

### 4D. Correct legal/support metadata

**Primary findings:** QR-025, QR-026, QR-028, QR-032, QR-033.

Choose/commit the project license, correct package homepage, document third-party/upstream provenance, add security/contribution/changelog policies, remove or document unrelated fixtures, and publish a truthful supported-target matrix. Replace “any size”/constant-cost/never-mutates claims with tested bounds and scoped invariants.

## Wave 5 — Accessibility, UX resilience, and maintainability

This wave can proceed in parallel after the P0 lifecycle model is stable; do not build accessibility fixes on dialogs/tabs that will be replaced by the coordinator.

### 5A. Accessibility and interaction baseline

**Primary findings:** FE-031 through FE-037, FE-042.

- Implement tabs with correct roles, selection, roving focus, arrow/Home/End/Delete/Enter behavior, visible dirty/close controls, and accessible names.
- Use a shared modal primitive with focus trap, initial focus, Escape policy, inert background, restoration, and safe destructive defaults.
- Provide keyboard equivalents for results, context menus, splitters, grids, encoding controls, and all pointer-only actions.
- Announce async status/progress/errors with appropriate live regions and move focus after navigation/destruction.
- Honor reduced motion and provide splash/empty states that do not require a mouse.
- Test at the 900×600 minimum, zoom/text scaling, long localized labels, high contrast, and screen-reader/keyboard paths.

### 5B. Decompose only after invariants are covered

**Primary findings:** AR-008 through AR-013, QR-024, QR-029.

Split the global store and backend monoliths along the session/resource/executor boundaries introduced above. Consolidate duplicated CSV/SQL stacks behind shared contracts. Generate typed event codecs. Lazy-load Monaco/xterm/tools and add measured budgets. Refactoring without characterization and regression tests will move bugs rather than remove them.

## Suggested ownership map

| Area | Primary owner profile | Mandatory reviewer |
| --- | --- | --- |
| Output transaction/recovery | Go storage/reliability | Independent data-integrity reviewer |
| Secrets/network/SQL policy | Application security + Go backend | DB/security specialist |
| Workspace lifecycle/jobs | Backend platform + frontend state | Concurrency reviewer |
| Dirty-resource coordinator | Frontend platform/UX | Backend resource owner + accessibility reviewer |
| Big-file format correctness | Streaming/data engineer | Data-integrity reviewer |
| CI/release/supply chain | Build/release engineer | Security owner |
| Version/licensing/docs | Release owner/product | Legal/project owner |
| Accessibility | Frontend accessibility owner | Keyboard/screen-reader tester |

No author should be the only reviewer of a P0 mutation, secret, recovery, or release-signing change.

## Required regression suites by invariant

| Invariant | Minimum automated proof |
| --- | --- |
| Source is never destroyed by an output operation | Same path, hard link, symlink alias, disk fault, cancel, parser error tests for every transformer |
| Saved state corresponds to exact edit revision | Deferred save completion with newer edit; failure; conflict; Save & Close; quit/switch paths |
| Workspace A cannot affect workspace B | Delayed responses/events/jobs/terminal/agent approvals across rapid session switches |
| Approval matches execution | Canonical intent digest mutation/replay/expiry/oversize tests |
| Secret belongs to its consuming object/purpose | Cross-profile/provider ref substitution and crash-between-writes tests |
| Recovery modifies only its authenticated pre-image | Foreign/tampered/truncated/replayed sidecars and interrupted recovery tests |
| Read-only SQL guard is not the only defense | Quoted identifiers, dangerous functions, server timeout, least privilege, row/byte caps on real DBs |
| Cancellation ends owned work | Process tree, LLM stream, DB query, transform, indexing, watcher/session teardown tests |
| Published tag identifies tested binary | Reusable workflow dependency plus platform metadata/runtime extraction |
| Official artifact is attributable | Signature/notarization verification, checksum signature, SBOM and provenance validation |

## Final release gate checklist

A release candidate may proceed only when:

- all Critical findings are closed with tests;
- P0 High findings are closed or the affected feature is removed/disabled in code and documentation;
- no transform can open/truncate an input identity as output;
- all unsaved/staged resources participate in close/switch/quit;
- agent, network, DB, command, and model output limits are enforced before buffering;
- server mode is absent or independently authenticated/sandboxed and security-tested;
- Windows CI cannot false-green and does not omit jobs;
- the tag commit passes reusable validation and builds from a clean tree;
- runtime/platform versions match the tag;
- release artifacts pass launch/smoke, signature/notarization, SBOM, provenance, and checksum verification;
- documentation no longer makes guarantees broader than the tests and implementation.
