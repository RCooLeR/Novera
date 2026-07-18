# Testing, Build, Release, Supply Chain, and Maintainability Review

## Executive summary

The checked revision can type-check, lint, unit-test, and production-build its frontend on this Windows workstation, and most backend packages have substantial unit coverage—particularly the large-file core. However, the delivery system does not yet provide evidence strong enough for the product’s data-integrity and privileged-operation claims. The most dangerous workflows cross the Go/JavaScript boundary and are almost entirely untested as workflows; the Windows CI test loop can mask package failures; release jobs build and publish without rerunning validation; and release tags do not control the version embedded in artifacts.

This review records **39 findings: 10 High, 28 Medium, and 1 Low**. The highest-priority delivery blockers are:

- disable or repair the non-building server/Docker target without enabling its remote security exposure;
- make release builds depend on the exact commit’s full test/security gates;
- derive all package/runtime versions from the release tag and verify the built artifacts;
- fix the Windows test command so any package failure fails the job and cover `internal/jobs` instead of permanently excluding it;
- add end-to-end tests for save/close/switch/quit, staged large-file edits, transform aliasing/cancellation, agent approvals, and workspace-generation races;
- verify and pin executable build inputs, and sign/notarize plus attest released binaries.

## Validation performed

The review used repository inspection plus bounded local checks. A passing command demonstrates only the scope named below; it does not negate the adversarial findings in the implementation audits.

| Check | Result | Important limitation |
| --- | --- | --- |
| `go version` | `go1.26.5 windows/amd64` | Slightly newer patch than the `go1.26.4` toolchain directive/CI |
| `go mod verify` | Passed | Integrity against downloaded module hashes; not a vulnerability scan |
| `go mod tidy -diff` | Passed with no diff | Does not justify running `tidy` as a production build mutation |
| `go vet ./...` | Passed | The scope also observes any Go packages under installed frontend dependencies; `vet` is not a race/security proof |
| `go test ./...` | All reached project packages passed except `novera/internal/jobs`, whose generated test executable was blocked by Microsoft Defender as “virus or potentially unwanted software” | The same environmental failure explains the checked-in Windows CI exclusion; it must be diagnosed, not normalized. `./...` also discovered a Go package under `frontend/node_modules/flatted` after npm install |
| Targeted `go test` over workspace, sqlguard, datatools, DB, watcher and large-file packages | Passed | Existing tests do not include the adversarial repros documented in the backend report; watcher has no tests |
| Go coverage attempts | Inconclusive: repeated broad coverage runs stalled or hit the Defender behavior | No percentage is claimed in this review |
| `go run golang.org/x/vuln/cmd/govulncheck@latest ...` and binary scan | Inconclusive: source, binary, and even version invocations did not produce a result within bounded five-minute runs | This is a missing verification result, not evidence of a Go vulnerability |
| `npm ls --depth=1` | Passed | Dependency graph resolution only |
| `npm run typecheck` | Passed | Static TypeScript types do not validate event payloads or async ordering |
| `npm run lint` | Passed | CI does not run it |
| `npm run test` | Passed: 1 file, 5 tests | Tests are helper-level, not component or application workflows |
| `npm run build` | Passed | Vite warned about chunks over 500 KiB; large worker/editor chunks are listed in QR-029 |
| `npm audit --omit=dev` using the system CA | 0 production findings | Does not cover build/test tooling |
| Full `npm audit` | 5 development findings: 3 moderate, 1 high, 1 critical | Installed Vite/Vitest/esbuild versions are affected; exposure is primarily developer/CI server tooling, not the shipped static bundle |
| `go build -tags "server production"` on Windows | Failed inside Wails alpha.79 with duplicate platform constructors/undefined `windowsApp` | Confirms advertised server target is broken in this revision |
| `CGO_ENABLED=0 GOOS=linux go build -tags "server production"` | Failed inside Wails alpha.79 because the server `BrowserWindow` does not satisfy the application interface (`SetScreen` missing) | Independent second target failure |
| Standard frontend production build | Passed | A full packaged desktop launch/smoke test was not performed in this review environment |

### Test inventory snapshot

- 262 Go files were discovered outside `frontend/node_modules`.
- 105 Go test files contain approximately 691 `Test*` functions and 12 fuzz entry points.
- No Go `Benchmark*` functions were found.
- 94 TypeScript/TSX files were found across `frontend/src` and generated bindings; `frontend/src` contains 39 TSX files.
- Only one frontend test file exists, with five passing tests.
- Project packages without a `_test.go` file: root, `internal/bigfile/highlight`, `internal/bigfile/session`, `internal/gitsvc`, `internal/llm`, `internal/netsafe`, `internal/paths`, `internal/secret`, `internal/terminal`, and `internal/watcher`.

## Findings

### QR-001 — The advertised server build target does not compile on either checked platform path

**Severity:** High
**Confidence:** High

**Evidence**

- `Taskfile.yml:42-50` advertises server build/run commands; `build/Taskfile.yml:126-147` describes a “pure HTTP server without native GUI dependencies”.
- A Windows `go build -tags "server production"` failed inside Wails alpha.79 due to duplicate `newClipboardImpl`, dialog, platform lock, system tray, and window constructors plus an undefined `windowsApp`.
- A `CGO_ENABLED=0 GOOS=linux` build failed because the server browser-window implementation lacks `SetScreen` required by the application interface.
- The repository pins `github.com/wailsapp/wails/v3 v3.0.0-alpha.79` at `go.mod:14-16`; this is not a transient `latest` mismatch.
- Even if compilation is repaired, the distroless image contains no native dialog implementation, Git executable, ordinary shell, or Linux Secret Service/keyring. Yet `main.go:79-93` registers dialog-dependent shell helpers, Git, terminal, secret-consuming providers, and the rest of the desktop graph unchanged. Headless behavior is therefore undefined/broken in addition to unsafe.

**Impact**

A documented first-class build command is unusable. Repairing it casually is also dangerous because **AR-001** shows that the desktop privilege graph is unsafe to expose over the default HTTP transport.

**Recommendation**

Remove the server tasks from the supported command surface now. If a server product is desired, first create a separate composition root and threat model, then pin a Wails revision with a passing minimal server fixture. Add compile and authenticated smoke tests to CI on every supported OS/architecture before restoring the task.

### QR-002 — The server Dockerfile cannot build from a clean checkout

**Severity:** Medium
**Confidence:** High

**Evidence**

- `build/docker/Dockerfile.server:12-22` copies the checkout and immediately runs `go mod tidy` and `go build`.
- It has no Node stage and never runs `npm ci` or `npm run build`.
- The Go binary embeds `frontend/dist`; that directory is ignored at `.gitignore:9` and therefore absent from a clean clone.
- The Dockerfile later tries to copy `/app/frontend/dist` at `build/docker/Dockerfile.server:30-31`, but nothing created it.

**Impact**

The image can appear to work only from a dirty local context that happens to contain ignored build output. It is not a reproducible source build and can silently package stale frontend assets unrelated to the checked-out commit.

**Recommendation**

Keep the image disabled with server mode. If restored, use a pinned Node builder, `npm ci`, a production frontend build, and an explicit copy of only lockfiles/source before the Go builder. Verify the embedded asset hash against the source commit and run the Docker build from an exported clean archive in CI.

### QR-003 — A release tag controls filenames but not the version embedded in the application or packages

**Severity:** High
**Confidence:** High

**Evidence**

- `.github/workflows/release.yml:89`, `95`, and `107` interpolate `github.ref_name`/`GITHUB_REF_NAME` only into archive names.
- Application/package versions remain hardcoded as `0.1.0` in `build/config.yml:14`, `frontend/package.json:4`, `build/windows/wails.exe.manifest:3`, `build/windows/info.json:3,7`, macOS plists at `build/darwin/Info.plist:19-22`, MSIX manifests, iOS files, and `build/linux/nfpm/nfpm.yaml:9`.
- Android independently declares `versionName "1.0"` at `build/android/app/build.gradle:18`.
- The only runtime build-info package defaults to `ApplicationName = "Quarry Editor"` and `Version = "0.0.0-dev"` (`internal/bigfile/buildinfo/buildinfo.go:8-12`) and is not wired into application UI or linker flags.
- Production build linker flags in `build/windows/Taskfile.yml:51`, `build/darwin/Taskfile.yml:44`, and `build/linux/Taskfile.yml:55` strip symbols but inject no version, commit, or date.

**Impact**

A `v0.2.0` or `v1.0.0` release would still identify itself and install as 0.1.0 in multiple ecosystems. Upgrade/downgrade behavior, support diagnostics, vulnerability mapping, crash reports, and user trust become unreliable.

**Recommendation**

Use one validated semantic version source derived from the tag in release CI. Generate platform metadata or pass it to packaging tasks; inject version, commit, and reproducible build timestamp through linker variables; show it in About and diagnostics. Add an artifact-inspection step that reads PE version resources, macOS `Info.plist`, Linux package metadata, and runtime `--version`/About output and rejects any mismatch with the tag.

### QR-004 — Release jobs publish without rerunning or depending on the validation workflow

**Severity:** High
**Confidence:** High

**Evidence**

- `.github/workflows/release.yml:3-6` triggers directly for every `v*` tag.
- The `package` job (`17-114`) installs dependencies and builds immediately; it runs no Go tests, frontend typecheck/tests/lint, vet, audit, generated-diff check, or smoke test.
- The publish job depends only on `package` (`116-120`). It does not depend on a CI workflow result for the same commit.
- CI is branch/PR triggered, not a required reusable workflow invoked by release (`.github/workflows/ci.yml:3-8`). A tag can point to a commit that never passed the branch checks.
- GoReleaser publishes as a non-draft release with `make_latest: true` (`.goreleaser.yaml:23-30`).

**Impact**

A typo, failing test, known vulnerable dependency, stale generated binding, or manually tagged unreviewed commit can become the latest public release. Native builds succeeding is the only effective gate.

**Recommendation**

Extract validation into a reusable workflow and call it from both CI and release. Require the release to build from the same immutable commit only after tests, lint/vet, vulnerability checks, generated-code diff, clean-tree build, package smoke tests, and artifact metadata verification pass. Use protected release environments and initially publish a draft for explicit promotion.

### QR-005 — The Windows CI package loop can mask failures and permanently excludes `internal/jobs`

**Severity:** High
**Confidence:** High

**Evidence**

- `.github/workflows/ci.yml:85-93` pipes `go list` through `ForEach-Object { go test $_ }`.
- PowerShell continues to later iterations after a native command exits nonzero. A later successful `go test` can replace `$LASTEXITCODE`, leaving the step successful. An analogous bounded PowerShell experiment (failing native command followed by a successful one in the pipeline) ended with exit code 0.
- Lines `91-93` explicitly remove `novera/internal/jobs` on every Windows run.
- Local execution showed why—the package test executable was blocked by Microsoft Defender as a potential unwanted application—but the repository contains no issue link, diagnostic, alternative execution method, or separate required runner for it.

**Impact**

Windows-specific failures can be false-green, and the jobs package can regress indefinitely on the primary platform. Jobs contains cancellation, lifecycle, log retention, and eviction behavior used by agent and data operations.

**Recommendation**

Prefer one aggregate command with an explicit project package list. If iteration is required, check `$LASTEXITCODE` after each package and `exit` immediately; also fail when `go list` fails. Diagnose the Defender signature using retained test binary hashes and Microsoft submission/CI runner evidence. Until resolved, run `internal/jobs` on a separate required Windows environment or refactor the test/build pattern—never silently exclude it.

### QR-006 — Release-critical cross-boundary workflows have no automated end-to-end coverage

**Severity:** High
**Confidence:** High

**Evidence**

- The frontend has one test file and five helper tests; no rendered component or Wails bridge workflow tests were found.
- Go tests exercise many pure/backend paths but cannot validate UI ordering, tab lifecycle, menu behavior, native close, focus, or the generated JavaScript bridge.
- The critical data-loss flows **FE-001** through **FE-010**, same-source/output transform path **BE-003/FE-007**, approval mismatch **FE-022**, and workspace-result races **FE-009** are cross-layer sequences.
- CI only runs Go unit tests, TypeScript typecheck, Vitest helper tests, and a build (`.github/workflows/ci.yml:81-103`).

**Impact**

The project can be fully green while save-and-close discards data, workspace switching accepts stale work, staged large-file edits vanish, or a user approves a different mutation than is executed. These are precisely the behaviors that matter most in an editor/workbench.

**Recommendation**

Build a bridge test harness with deterministic fake services and an application-level browser test layer. Required scenarios should cover: edits during an in-flight save; failed Save & Close; every tab/workspace/window quit path; large-file stage/save/follow-tail; source/destination alias and hard link; workspace switch with delayed old responses; terminal teardown; transform cancel/partial output; agent exact-intent approval; DB profile switching; and keyboard/focus accessibility. Run them on all desktop platforms, with a smaller packaged-app smoke set on real Wails binaries.

### QR-008 — Installed development tooling has five known npm advisories, including a critical Vitest issue

**Severity:** Medium
**Confidence:** High as of 2026-07-16

**Evidence**

- Full `npm audit` reported 5 development findings: 3 moderate, 1 high, and 1 critical.
- The installed graph contains Vite 5.4.21 and Vitest 2.1.9 under permissive manifest ranges (`frontend/package.json:37-38`).
- Reported issues include an esbuild development-server request weakness; Vite path traversal/UNC/Windows filesystem restriction bypasses; and a critical Vitest UI-server arbitrary file read/execution advisory.
- `npm audit --omit=dev` returned zero, so these are not reported in the shipped runtime dependency set.

**Impact**

Risk is concentrated in developer and CI server contexts rather than the compiled static application. It still matters when a developer runs Vite/Vitest against untrusted content or binds a dev server beyond loopback. Automated tooling also remains red, hiding future changes.

**Recommendation**

Upgrade Vite/Vitest/esbuild through tested supported major versions, re-run frontend tests/build, and document loopback-only dev-server policy. Gate `npm audit --omit=dev` strictly and track dev-only findings with an expiry rather than ignoring the audit wholesale. Do not expose Vite/Vitest UI servers on untrusted networks.

### QR-009 — Go vulnerability status is not established or gated

**Severity:** Medium
**Confidence:** High for the missing assurance; no Go vulnerability is alleged

**Evidence**

- No `govulncheck` step exists in `.github/workflows/ci.yml` or the release workflow.
- Bounded source and built-binary scans using the current `golang.org/x/vuln/cmd/govulncheck` did not complete or return output on this host within five minutes; even the tool version invocation stalled.
- `go mod verify` passed, but it verifies module content hashes rather than known vulnerability reachability.

**Impact**

The review cannot responsibly claim the Go dependency graph is vulnerability-free. Future reachable advisories can ship without a required signal.

**Recommendation**

Run a pinned `govulncheck` version on a clean Linux CI runner with a timeout and retained JSON/SARIF output. Scan both source and release binaries where feasible. Treat reachable standard-library/direct dependency findings as release blockers and record reviewed exceptions with an expiry.

### QR-010 — CI omits important static, race, coverage, fuzz, and dependency gates

**Severity:** Medium
**Confidence:** High

**Evidence**

- CI runs Go tests, frontend typecheck/tests, and a production build (`.github/workflows/ci.yml:81-103`).
- It does not run `go vet`, a Go linter/staticcheck, `go test -race`, coverage thresholds/diffs, fuzz seeds, `npm run lint`, npm audit, `govulncheck`, secret scanning, or generated-code cleanliness.
- Local `go vet ./...`, frontend lint, typecheck, tests, and build passed, demonstrating that at least lint/vet are currently adoptable without a cleanup campaign.

**Impact**

Regressions detected by these already-clean checks can merge. Race-prone packages such as watcher, sessions, jobs, workspace, agent, and DB receive no required dynamic concurrency signal.

**Recommendation**

Add fast required gates first: scoped `go vet`, frontend lint, `npm audit --omit=dev`, `go mod tidy -diff`, binding diff, and vulnerability scan. Add targeted `-race` jobs for concurrency-heavy packages on Linux and Windows where supported, nightly fuzz runs with saved corpus, and coverage reporting focused on changed critical code rather than a vanity global threshold.

### QR-011 — Frontend tests do not cover components, accessibility, state races, or error handling

**Severity:** Medium
**Confidence:** High

**Evidence**

- One frontend test file with five tests exists for 39 TSX source files and a 1,823-line store.
- No React Testing Library/component harness, automated accessibility scanner, fake-timer async race tests, or visual regression suite was found.
- **FE-031** through **FE-037** identify keyboard, focus, announcement, motion, and responsive layout failures that typecheck/lint cannot detect.

**Impact**

UI correctness depends on manual exploration, and accessibility regressions have no machine-enforced baseline. Async last-response-wins failures are especially easy to reintroduce.

**Recommendation**

Test pure store reducers and request-generation behavior first, then components with a mocked typed bridge. Add axe-based checks plus explicit keyboard/focus assertions; axe alone is insufficient. Add visual snapshots at minimum supported window sizes and reduced-motion settings. Make every fixed FE finding carry a regression test.

### QR-012 — Ten project packages have no package-local Go tests

**Severity:** Medium
**Confidence:** High

**Evidence**

No `_test.go` file was found in the root package or `internal/bigfile/highlight`, `internal/bigfile/session`, `internal/gitsvc`, `internal/llm`, `internal/netsafe`, `internal/paths`, `internal/secret`, `internal/terminal`, and `internal/watcher`.

Several are high-risk boundaries: `netsafe` controls endpoint safety, `paths` controls containment, `secret` stores credential material, `terminal` manages child processes, and `watcher` has a confirmed shutdown race (**BE-037**).

**Impact**

Core safety assumptions are protected only indirectly, if at all. Package behavior can change while downstream happy-path tests remain green.

**Recommendation**

Prioritize table/adversarial tests for path aliases/symlinks, DNS/IP rebinding policy, secret corruption/key rotation, terminal process-tree cancellation/output limits, and watcher start/stop races. Add race tests and injectable OS/network boundaries. Root composition deserves a binding allowlist and production-option test rather than a conventional unit test.

### QR-013 — Database and provider behavior lacks real-engine/protocol integration coverage

**Severity:** Medium
**Confidence:** High

**Evidence**

- Repository DB tests validate DSN/default construction and local logic, but no CI service containers for PostgreSQL or MySQL were found.
- The backend audit found behavior that unit mocks/default assertions did not catch: unauthenticated remote TLS defaults (**BE-022**), quoted-identifier SQL guard bypass (**BE-007**), unbounded query results (**BE-021**), and dialect identity issues (**BE-035**).
- LLM, agent, Git, and terminal protocols similarly lack required fake-server/real-process integration jobs.

**Impact**

Driver semantics, TLS verification, SQL dialect parsing, cancellation, streaming framing, redirect behavior, and process-tree behavior can differ from local assumptions.

**Recommendation**

Run ephemeral PostgreSQL/MySQL with generated CA/server certificates and least-privileged users; assert verified TLS, timeouts, read-only enforcement, quoted dangerous function rejection, row/byte caps, and cancellation. Add deterministic HTTP/SSE fake providers for fragmented/malformed/unbounded LLM responses, temporary real Git repositories, and process-tree terminal fixtures on each OS.

### QR-014 — No benchmarks or performance regression budgets support the large-file claims

**Severity:** Medium
**Confidence:** High

**Evidence**

- No `Benchmark*` functions were found despite extensive large-file code and tests.
- `README.md:19-23,39-40` and `docs/big-files.md:3-5,23-25` make strong arbitrary-size and constant-cost claims.
- Several backend findings concern unbounded lines, query results, responses, command output, and rollback buffers (**BE-008**, **BE-009**, **BE-011**, **BE-012**, **BE-020**, **BE-021**).

**Impact**

Memory/latency regressions can ship undetected, and the marketing claims cannot be tied to measured hardware, encodings, line distributions, cache settings, or output size.

**Recommendation**

Create reproducible benchmarks for open/window/search/index/stage/save and each streaming transform over sparse files and generated fixtures, including giant single lines and pathological encodings. Track peak RSS, allocations, throughput, cancel latency, and disk amplification. Set budgets per operation and run a stable subset in CI plus full nightly performance jobs.

### QR-015 — The documented “all backend tests” command includes installed frontend dependency packages

**Severity:** Medium
**Confidence:** High

**Evidence**

- `README.md:96-100` recommends `go test ./...`.
- After `npm install`, that pattern discovered `frontend/node_modules/flatted/golang/pkg/flatted`, which is not Novera-owned code.
- CI correctly uses `. ./internal/...` on non-Windows (`.github/workflows/ci.yml:81-83`), so documentation and automation already disagree.

**Impact**

Developer results depend on whether frontend dependencies are installed and can fail or slow down due to third-party Go fixtures. It also obscures which packages the project actually guarantees.

**Recommendation**

Document and script an explicit project scope such as `go test . ./internal/...`. Use one task invoked by README, CI, and release. Consider a workspace layout or tooling check that prevents nested dependency fixtures from entering Go package patterns.

### QR-016 — Normal builds mutate dependency and generated-source state

**Severity:** Medium
**Confidence:** High

**Evidence**

- `build/Taskfile.yml:4-8` defines `go:mod:tidy` as a command, not a check.
- Binding generation depends on it (`build/Taskfile.yml:83-98`); Windows/macOS/Linux native production builds also depend on it (`build/windows/Taskfile.yml:34-45`, `build/darwin/Taskfile.yml:32-42`, `build/linux/Taskfile.yml:42-53`).
- Binding generation runs with `-clean=true` and writes tracked `frontend/bindings` as a build dependency.
- CI does not assert that the tree remains unchanged after build.

**Impact**

A release build can silently alter `go.mod`, `go.sum`, or generated bindings and then package a state different from the tagged commit. Local builds can overwrite user changes. Generated drift may be hidden instead of reviewed.

**Recommendation**

Separate `generate`/`tidy` mutations from `check`/`build`. CI should run generators in a clean checkout and fail on `git diff --exit-code`; production build should consume checked-in or explicitly staged generated artifacts without rewriting source. Use `go mod tidy -diff` as the gate.

### QR-017 — Build tasks use `npm install` instead of lockfile-strict `npm ci`

**Severity:** Medium
**Confidence:** High

**Evidence**

- `build/Taskfile.yml:10-22` runs `npm install` for frontend dependencies.
- `build/docker/Dockerfile.cross:175-177` also runs `npm install --silent` when `frontend/dist` is absent.
- CI’s explicit frontend check uses `npm ci` at `.github/workflows/ci.yml:95-100`, but the subsequent `task build` can invoke the looser task again.

**Impact**

Builds may resolve or rewrite dependencies instead of failing on lockfile inconsistency. Local, Docker, CI, and release dependency installation semantics differ.

**Recommendation**

Use `npm ci --ignore-scripts` where compatible, then explicitly allow only required lifecycle scripts. Cache the npm download cache rather than `node_modules`. Fail on lockfile changes and use the exact same install task in development bootstrap, CI, Docker, and release.

### QR-018 — Cross-build containers execute downloaded toolchains/SDKs without checksum or signature verification

**Severity:** High
**Confidence:** High

**Evidence**

- `build/docker/Dockerfile.cross:27-32` streams a Zig archive from the network directly into `tar`.
- Lines `34-38` do the same for a third-party-hosted macOS SDK archive from GitHub.
- No SHA-256, signature, provenance, immutable digest, or vendored checksum is checked before the extracted content becomes the compiler/sysroot.
- The base image is referenced by mutable tag `golang:1.25-bookworm` at line 16.

**Impact**

Compromise of a download origin, release account, DNS/TLS path, or mutable artifact can inject code into every cross-built binary. Because these are compilers and SDK files, ordinary source review and module checks will not detect it.

**Recommendation**

Pin base images by digest. Download to a file, verify a repository-reviewed SHA-256 and upstream signature where available, then extract. Prefer a controlled builder image produced by its own attested workflow. Record compiler/SDK digests in release provenance and periodically rotate them through reviewed dependency updates.

### QR-019 — No `.dockerignore` protects build context correctness or confidentiality

**Severity:** Medium
**Confidence:** High

**Evidence**

- No `.dockerignore` exists at the repository root.
- `build/docker/Dockerfile.server:12-13` uses `COPY . .` with the root as build context (`build/Taskfile.yml:149-156`).
- The working tree commonly contains `.git`, `frontend/node_modules`, ignored `frontend/dist`, `bin`, IDE state, temporary datasets, and developer configuration beyond the source needed for the image.

**Impact**

Docker sends a large and machine-dependent context to the daemon. Ignored/local files can influence layers, leak into remote builders/cache metadata, or make a supposedly clean image work only on one workstation. Git history and local datasets are unnecessary build inputs.

**Recommendation**

Add a restrictive root `.dockerignore` that starts from denial and admits only manifests, required source/assets, and build configuration. Avoid `COPY . .`; copy lockfiles/manifests first, then named source directories. In CI, inspect the context from `git archive` or a clean checkout and fail if untracked input is required.

### QR-020 — Release artifacts are unsigned/unnotarized and have no SBOM or provenance attestation

**Severity:** High
**Confidence:** High

**Evidence**

- Windows release packaging copies `bin/Novera.exe` directly into a zip (`.github/workflows/release.yml:80-89`); no Authenticode step is present.
- The macOS packaging task performs only an ad-hoc signature (`build/darwin/Taskfile.yml:151-157`); the release does not use a Developer ID identity, notarize, staple, or verify Gatekeeper acceptance.
- Linux publishes a raw tarball (`97-107`) without package/repository signing.
- Checksums are generated at `133-135`, but they are uploaded by the same workflow and are not signed.
- No SBOM, SLSA provenance, GitHub artifact attestation, dependency manifest, or reproducibility statement is produced.
- Platform Taskfiles contain signing comments/placeholders, but the release workflow does not activate them.

**Impact**

Users cannot distinguish official binaries from modified ones using platform trust mechanisms. A compromised release workflow/account can replace both artifact and checksum. Incident response cannot easily enumerate shipped dependencies.

**Recommendation**

Use protected hardware-backed/managed signing identities: Authenticode with timestamping, Apple Developer ID signing plus hardened runtime/notarization/stapling (not the current ad-hoc identity), and an appropriate Linux signing/distribution strategy. Generate CycloneDX or SPDX SBOMs, GitHub artifact attestations/SLSA provenance, and signed checksums. Verify every signature and launch the signed artifact before publication.

### QR-021 — GitHub Actions are pinned only to moving major-version tags

**Severity:** Medium
**Confidence:** High

**Evidence**

- Workflows use `actions/checkout@v4`, `actions/setup-go@v5`, `actions/setup-node@v4`, upload/download-artifact `@v4`, and `goreleaser/goreleaser-action@v7` (`.github/workflows/ci.yml:24-58`; `.github/workflows/release.yml:34-45,110-138`).
- None is pinned to a full commit SHA.
- Release actions receive write permission to repository contents (`.github/workflows/release.yml:8-9`).

**Impact**

A moved/compromised upstream tag changes executable workflow code without a Novera commit. The consequence is greatest in the release job with write permission and access to signing/release credentials once added.

**Recommendation**

Pin third-party and GitHub-owned actions to reviewed full commit SHAs, retaining a version comment. Use Dependabot/Renovate to propose controlled updates. Minimize token permissions per job, add protected environments for signing/publish, and avoid secrets in untrusted PR contexts.

### QR-022 — Dependency updates and vulnerability response are manual

**Severity:** Medium
**Confidence:** High

**Evidence**

- No `.github/dependabot.yml` or Renovate configuration exists.
- Go and npm manifests include a wide framework/tooling surface, including a pinned Wails alpha and npm caret ranges.
- The current npm development audit is already red, while no CI audit or tracked exception file exists.

**Impact**

Security and compatibility updates rely on maintainers remembering every ecosystem. Large, infrequent upgrades become riskier, and advisory response time is unknowable.

**Recommendation**

Configure scheduled grouped update PRs separately for Go runtime dependencies, Wails/runtime pair, frontend runtime dependencies, development tooling, Actions, and Docker base images. Require the complete validation suite and label security updates. Establish an SLA/exception record with owner and expiry for advisories that cannot be fixed immediately.

### QR-023 — Go version requirements disagree across the module, documentation, CI, and container builders

**Severity:** Medium
**Confidence:** High

**Evidence**

- `go.mod:3-5` declares language version 1.25.0 but a `go1.26.4` toolchain.
- CI and release explicitly install 1.26.4 (`.github/workflows/ci.yml:12-16`; `.github/workflows/release.yml:11-15`).
- `README.md:48,58` tells developers Go 1.25+ is sufficient.
- `build/docker/Dockerfile.cross:16` starts from Go 1.25, relying on automatic toolchain download to satisfy the directive; `Dockerfile.server` uses mutable `golang:alpine` with an unspecified Go version.
- The review host used 1.26.5.

**Impact**

Offline builds can fail unexpectedly, toolchain downloads add an unreviewed network step, and developers following the README do not reproduce CI exactly.

**Recommendation**

Choose and document one supported Go toolchain policy. For reproducible builds, pin exact builder image digests containing the required toolchain, set `GOTOOLCHAIN=local`, and fail clearly on mismatch. If Go 1.25 compatibility is intentional, test it as a matrix and remove the forced 1.26 toolchain; otherwise update the README prerequisite. Replace the Wails CLI `@latest` command in `README.md:60-64` with the same pinned version used by the module/CI.

### QR-024 — The core desktop/runtime framework remains a pre-stable alpha without a contained upgrade strategy

**Severity:** Medium
**Confidence:** High

**Evidence**

- Backend and frontend runtime are both pinned to Wails `3.0.0-alpha.79` (`go.mod:14-16`, `frontend/package.json:17`).
- A comment recognizes that the alpha API can break, but no issue/milestone, compatibility adapter, or framework smoke suite is present.
- The current server build failures originate in platform/build-tag combinations of that pinned dependency (**QR-001**).
- Application code directly imports Wails application/event APIs in many services, increasing migration surface.

**Impact**

Framework bugs and breaking changes can block security fixes or platform updates. Direct coupling makes a later upgrade broad and difficult to validate.

**Recommendation**

Track a concrete stable-migration milestone. Wrap event emission, dialogs/windows, service exposure, and runtime calls behind small adapters; maintain a minimal platform fixture exercising production build, events, dialogs, lifecycle, and service bindings. Upgrade alpha versions in isolated PRs with all three desktop package smoke tests.

### QR-025 — The repository has no project license while Linux package metadata declares MIT and the wrong homepage

**Severity:** Medium
**Confidence:** High

**Evidence**

- No root `LICENSE` or `LICENSE.md` exists. The only obvious bundled license belongs to a frontend font/style asset.
- `build/linux/nfpm/nfpm.yaml:16` declares the application license as MIT.
- The same package metadata sets `homepage: "https://wails.io"` at line 15, which is the framework site rather than Novera’s project/release site.
- Release archives include the README but cannot include a missing application license (`.github/workflows/release.yml:84-107`).

**Impact**

Users and contributors have no granted rights in the repository despite package metadata claiming a license. Package indexes would publish misleading legal and project-origin information.

**Recommendation**

Have the copyright owner choose and commit the intended license; do not infer it solely from nfpm metadata. Add third-party notices as required, correct homepage/source/bug URLs, and include the license/notices in every installer/archive. Add a packaging test that inspects metadata and archive contents.

### QR-026 — Security, contribution, disclosure, and change-management documentation is absent

**Severity:** Medium
**Confidence:** High

**Evidence**

- No `SECURITY.md`, `CONTRIBUTING.md`, `CHANGELOG.md`, `CODE_OF_CONDUCT.md`, issue/PR templates, or release checklist was found.
- The application handles credentials, executes commands, accesses databases, and mutates user files, so security-report routing and support boundaries materially matter.
- GoReleaser auto-generates notes but filters some commit classes (`.goreleaser.yaml:15-21`); generated notes are not a migration/deprecation/security changelog.

**Impact**

Researchers lack a private reporting route, contributors lack reproducible validation instructions, and users cannot reliably determine breaking changes, data migrations, or fixed vulnerabilities.

**Recommendation**

Add a supported-version/security-disclosure policy, contribution guide that invokes canonical tasks, code review/release checklists, issue templates, and a curated changelog. Document data-format migrations and security-impacting changes explicitly. Keep sensitive contact details maintainable and monitored.

### QR-027 — Users and support cannot identify the running build

**Severity:** Medium
**Confidence:** High

**Evidence**

- The About UI reports technology/product information but no injected version, commit, channel, or build date.
- `internal/bigfile/buildinfo/buildinfo.go:8-12` retains upstream `Quarry Editor`/`0.0.0-dev` defaults and is not connected to the application shell.
- Production build flags do not inject identity (**QR-003**) and use `-buildvcs=false`, removing automatic VCS settings.

**Impact**

Bug reports and security advisories cannot be tied reliably to a binary. Users cannot confirm that an update installed or distinguish stable/prerelease/custom builds.

**Recommendation**

Create one application build-info package with product name, semantic version, commit, build channel, reproducible timestamp, dirty flag for local builds, Go/Wails versions, and update provenance. Expose a safe read-only DTO to About/diagnostics and add a command-line/version-resource verification test.

### QR-028 — Large-file documentation makes unbounded guarantees and links to an unspecified upstream

**Severity:** Medium
**Confidence:** High

**Evidence**

- `docs/big-files.md:3-6` claims files of “any size” and links `Quarry` to the generic `https://github.com/` home page rather than an identifiable source/version/license.
- Lines `23-25` claim a 400 GB file “costs the same as a 4 KB file”. Indexes, caches, filesystem APIs, huge lines, transforms, output, and platform limits make that literally false.
- Line 49-50 says source files are “never silently mutated”, while the backend/frontend audits demonstrate same-path transform truncation, automatic adjacent recovery replay, and staged-edit lifecycle loss.
- `README.md:19-23,39-40` repeats “any size” claims.

**Impact**

Users may entrust irreplaceable data to guarantees the code and test evidence do not sustain. Upstream provenance/license/patch tracking cannot be verified from the link.

**Recommendation**

Replace absolutes with tested limits and scoped invariants: bounded-window viewing under specified line/encoding conditions; transformation memory proportional to documented structures; disk space/output requirements; and backup guidance. Link the exact upstream repository, revision, license, and local modifications, or remove the attribution if it cannot be substantiated. Update documentation as critical findings are fixed.

### QR-029 — The frontend production build contains very large eager editor/worker chunks

**Severity:** Medium
**Confidence:** High

**Evidence**

- Production build passed but Vite warned that chunks exceed 500 KiB.
- Observed uncompressed outputs included approximately: Monaco/editor main chunk 3.33 MB (852 KB gzip), TypeScript worker 6.01 MB, CSS worker 1.01 MB, HTML worker 668 KB, JSON worker 362 KB, editor worker 231 KB, xterm chunk 293 KB, and main application JS 369 KB (110 KB gzip).
- `frontend/src/main.tsx:3` eagerly imports the Monaco setup before rendering the application, even on the welcome screen or for table/big-file-only usage.
- **FE-038** describes the user-visible startup/memory consequence.

**Impact**

Startup parse/memory cost and packaged size are higher than necessary, particularly on lower-end systems. It also makes the splash duration hide work rather than measure/improve it.

**Recommendation**

Lazy-load Monaco and language workers when a text editor/diff first opens; map only required worker languages; lazy-load xterm and heavy modal/tool modules by feature. Track chunk budgets and cold-start measurements in CI. Do not split blindly—measure request/parse overhead in the embedded asset environment.

### QR-030 — Development CSP allowances are shipped unchanged in production

**Severity:** Medium
**Confidence:** High

**Evidence**

- `frontend/index.html:5-13` says websocket schemes are retained for Vite HMR, but the static meta policy is also included in production output.
- `script-src` permits both `'unsafe-inline'` and `'unsafe-eval'`; `connect-src` permits all `ws:` and `wss:` origins rather than only the dev server.
- Production build does not transform the policy by mode.
- No direct unsafe HTML sink was identified in this review; this finding is defense-in-depth and documentation accuracy, not an asserted XSS.

**Impact**

If a renderer injection occurs, broad script evaluation and websocket egress allowances make exploitation/data exfiltration easier. The comment’s statement that remote connect loads are blocked is inaccurate for websocket schemes.

**Recommendation**

Generate separate dev and production policies. Production should omit HMR websocket allowances, remove `unsafe-eval` if Monaco configuration permits, and use hashes/nonces or bundled styles/scripts where feasible. Add a build test that parses the final `dist/index.html` and rejects forbidden directives. Keep privileged authorization backend-side regardless of CSP.

### QR-031 — CI and release workflows have no explicit concurrency cancellation or job timeouts

**Severity:** Medium
**Confidence:** High

**Evidence**

- Neither `.github/workflows/ci.yml` nor `.github/workflows/release.yml` defines a top-level `concurrency` group.
- No jobs declare `timeout-minutes`.
- The project installs toolchains, compiles Wails on three OSes, and has locally observed scanner/coverage/test stalls, making bounded execution material.

**Impact**

Superseded pushes consume runners; hung builds/scanners can run until GitHub’s broad default limit; duplicate tag workflow behavior is less controlled. Release operational recovery becomes slower and costlier.

**Recommendation**

Cancel superseded CI by workflow/ref while never canceling a started release unintentionally. Set realistic per-job timeouts and shorter per-tool timeouts with diagnostic artifact upload. Use immutable tag/release concurrency keys to prevent two publishers for the same version.

### QR-032 — `all.cql` is a large, unreferenced domain-specific fixture with unclear provenance

**Severity:** Low
**Confidence:** Medium

**Evidence**

- Root `all.cql` is roughly 52 KB and contains a domain graph schema/data unrelated to Novera’s documented application domains (for example owners, vets, pets, and commerce concepts).
- Repository search found no code, task, test, or documentation reference to it.
- It has been present since the initial history, but no README header identifies it as a sample, test fixture, generated artifact, license, or expected input.

**Impact**

The file adds review noise and creates uncertainty about whether it is product data, a demo, or accidentally committed material. No sensitive content was established, so this is a hygiene/provenance gap rather than a leak allegation.

**Recommendation**

Confirm ownership/provenance. Remove it if unused; otherwise move it under a clearly named test/sample directory, minimize it, document its purpose and source/license, and ensure it contains only synthetic data.

### QR-033 — Several advertised build targets have no CI compile or package verification

**Severity:** Medium
**Confidence:** High

**Evidence**

- Root `Taskfile.yml:3-9` includes Windows, Darwin, Linux, iOS, and Android; `42-60` adds server and Docker.
- CI builds the desktop application on three runners but does not invoke server/Docker or mobile build tasks (`.github/workflows/ci.yml:36-103`).
- Release exercises Windows desktop, Darwin universal packaging, and Linux desktop archive only (`.github/workflows/release.yml:17-107`).
- The unchecked server path is already broken (**QR-001**), demonstrating actual drift.

**Impact**

Task presence implies functionality that may have been broken for months. Mobile/server template changes can silently drift in versions, APIs, dependencies, and security assumptions.

**Recommendation**

Publish a support matrix. Delete or hide unsupported generated templates. For every supported target, add at least compile, package structure, version, and launch/smoke validation; experimental targets should have non-required scheduled checks and explicit warnings. Do not count a template task as a supported product.

### QR-034 — The pipeline does not prove clean-checkout or reproducible build behavior

**Severity:** Medium
**Confidence:** High

**Evidence**

- Builds mutate tidy/bindings (**QR-016**) and Docker can consume ignored assets (**QR-002/QR-019**).
- Production flags use `-trimpath` and `-buildvcs=false`, which help path stability, but no second-build comparison or normalized artifact diff is performed.
- Mutable runner images (`macos-latest`, major-tag Actions, unpinned Docker bases) and unstated packaging tool versions remain build inputs.
- Release packages whatever the native task produces without inspecting embedded assets, version resources, architecture, or signatures.

**Impact**

There is no evidence that the tag alone is sufficient to recreate an artifact or that two builders produce equivalent content. Stale local/generated files can affect output without appearing in source review.

**Recommendation**

Build releases from a clean exported source tree with network inputs locked. Assert no git diff/untracked dependency after generation/build. Record all tool/image digests and `SOURCE_DATE_EPOCH`; compare repeat builds where platform formats permit, otherwise diff normalized contents. Inspect archive inventory, executable architecture, embedded frontend hash, metadata, and runtime version before signing/publishing.

### QR-035 — Repository security automation lacks secret scanning, code scanning, and policy enforcement

**Severity:** Medium
**Confidence:** High

**Evidence**

- Only CI and release workflows exist under `.github/workflows`; no CodeQL/static security workflow, dependency review action, secret scanner, scorecard, or artifact policy job was found.
- A manual pattern search during this review did not identify obvious committed credential strings, but that is a point-in-time best effort rather than history-aware prevention.
- The application’s privileged surface includes command execution, outbound network, databases, secrets, filesystem mutation, and updateable build tooling.

**Impact**

New secrets or common injection/path/query issues can merge without an automated signal. Dependency risk changes in a PR are not summarized, and history may contain material not visible at HEAD.

**Recommendation**

Enable GitHub secret scanning/push protection where available and add a local/CI scanner with reviewed rules. Add CodeQL for Go and JavaScript/TypeScript, dependency review on PRs, and targeted custom checks for bound-method allowlists, raw secret logging, output aliasing, and unsafe build downloads. Treat tools as signals requiring triage, not substitutes for the threat-model and adversarial tests in the backend review.

### QR-036 — The iOS package task copies an executable that its dependency never builds

**Severity:** High
**Confidence:** High

**Evidence**

- `build/ios/Taskfile.yml:66-81` defines the package task, depends on `build`, and then copies `bin/Novera` into the bundle.
- The referenced `build` task produces `bin/Novera.a` at `build/ios/Taskfile.yml:40-45`, not an executable named `Novera`.
- The separate `compile:ios` linker task at `117-137` is not a package dependency and emits lowercase `bin/novera`.
- `build/ios/Info.plist:7-8` expects the bundle executable to be `Novera`.

**Impact**

A clean iOS package fails or, worse, copies a stale desktop/previous executable left in `bin`. Even manually invoking the linker produces a filename inconsistent with the plist.

**Recommendation**

Decide whether iOS is supported. If it is, define one canonical link task producing the exact plist executable, make package depend on it, isolate per-target output directories, and verify Mach-O platform/architecture, bundle executable name, version, signing, and launch on a simulator/device in CI. Otherwise remove/hide the task.

### QR-037 — The task described as a production Android package builds a debug APK

**Severity:** High
**Confidence:** High

**Evidence**

- `build/android/Taskfile.yml:107-114` describes `package` as building a production APK but delegates to `assemble:apk`.
- `assemble:apk` at `122-128` runs `./gradlew assembleDebug`.
- `build/android/app/build.gradle:26-33` has a release build type, but the package path does not invoke it or establish release signing.

**Impact**

An artifact presented as production is debuggable and debug-signed, with different optimization/security/update identity. It is unsuitable for trustworthy distribution.

**Recommendation**

Wire production packaging to `assembleRelease` with protected release signing, ProGuard/R8 policy as intended, deterministic versionCode/versionName, and post-build assertions that `android:debuggable` is false and the signer is the release identity. Keep debug packaging explicitly named and separate.

### QR-038 — The Windows MSIX package task references a configuration file that does not exist

**Severity:** Medium
**Confidence:** High

**Evidence**

- `build/windows/Taskfile.yml:128-142` invokes MSIX generation with a root `wails.json` path.
- No `wails.json` exists in the repository.
- CI and release do not execute the MSIX path, so the broken reference is invisible.

**Impact**

The documented/package-selectable MSIX target fails from a clean checkout and may encourage maintainers to substitute an unreviewed local configuration.

**Recommendation**

Commit the correct Wails v3/MSIX configuration and validate identity, publisher, capabilities, version, architecture, manifest, signature, install/uninstall, and upgrade behavior; or remove the target until maintained. Add a clean package smoke job.

### QR-039 — The Linux ARM64 cross path uses the host-architecture C compiler with CGO enabled

**Severity:** High
**Confidence:** High

**Evidence**

- `build/docker/Dockerfile.cross:144-153` selects `CC=gcc` for both Linux ARM64 and AMD64 while only changing `GOARCH`.
- The script forces `CGO_ENABLED=1` at `172-173`.
- `build/linux/Taskfile.yml:63-83` runs the builder image normally and passes `arm64`; it does not use a target `--platform`, an ARM64 image, or an `aarch64-linux-gnu-gcc` cross compiler/sysroot.
- The Dockerfile’s own comment says Linux uses native GCC and suggests `--platform`, but the task does not implement that requirement.

**Impact**

AMD64-host-to-ARM64 Linux builds cannot correctly compile/link CGO using native AMD64 `gcc`. The advertised architecture may fail or produce invalid/mismatched objects, and there is no CI verification.

**Recommendation**

Use an explicit target cross toolchain/sysroot (`aarch64-linux-gnu-*`) or run a digest-pinned ARM64 builder under the target platform. Inspect the resulting ELF architecture and dynamic dependencies, then launch it on a real/emulated ARM64 smoke runner. Cover both architectures in the support matrix.

### QR-040 — Published desktop archives bypass maintained native package/install dependency paths

**Severity:** Medium
**Confidence:** High

**Evidence**

- Linux release creates a raw tarball containing the executable, README, icon, and optional desktop file (`.github/workflows/release.yml:97-107`).
- The nfpm package configuration declares required GTK/WebKit runtime dependencies (`build/linux/nfpm/nfpm.yaml:27-44`), but those `.deb`/`.rpm` package tasks are not used by release.
- Windows similarly publishes a raw executable zip (`.github/workflows/release.yml:80-89`) rather than the repository’s installer paths that can handle WebView/bootstrap and uninstall metadata.
- README prerequisites at `README.md:56-64` describe development tools, not Linux end-user native library requirements.

**Impact**

Users can download an “official” archive that does not declare/install required runtime libraries or provide standard upgrade/uninstall integration. This increases launch failures and support ambiguity.

**Recommendation**

Publish tested native installers/packages as the primary artifacts: signed Windows installer/MSIX and signed Linux packages with accurate dependencies, plus a clearly labeled portable archive if intentionally supported. Run clean-machine install, launch, upgrade, and uninstall smoke tests. Document runtime prerequisites for any raw archive.

## Positive delivery and quality controls

- Go modules and npm dependencies have committed integrity lock data (`go.sum`, `package-lock.json`), and `go mod verify`/`npm ls` succeeded.
- CI builds the desktop application on Windows, macOS, and Linux instead of assuming cross-compilation is enough.
- Frontend typecheck, lint, tests, and production build are currently clean.
- The large-file subsystem has unusually extensive unit and fuzz-oriented coverage for a young project; 105 Go test files and 12 fuzz entry points are a solid base.
- Generated TypeScript bindings are committed, which makes API review possible and revealed the internal-method exposure in this audit.
- Release assets receive SHA-256 checksums, even though signing/attestation is still needed.
- Production Go flags include `-trimpath`, and sensitive JSON files generally use restrictive modes.
- CI permissions are read-only for the ordinary workflow and do not expose release write permission to PR jobs.

## Recommended verification layers

### Per pull request — required and fast

1. Clean checkout and exact pinned toolchains.
2. `go mod tidy -diff`, generated binding/event schema regeneration, and `git diff --exit-code`.
3. Explicit-scope Go unit tests; targeted race tests; `go vet` and selected static analysis.
4. Frontend `npm ci`, typecheck, lint, unit/component tests, accessibility tests, and production build with chunk/CSP assertions.
5. `govulncheck`, `npm audit --omit=dev`, dependency review, secret scan, and CodeQL/delta checks.
6. Cross-boundary headless workflow tests with fake typed services.
7. Native desktop compile on all supported OSes.

### Nightly/scheduled

- Full race and fuzz corpus runs.
- Real PostgreSQL/MySQL TLS/read-only/cancellation matrix.
- LLM streaming/redirect/malformed-response fake-server suite.
- Large sparse/pathological file performance, memory, cancellation, and disk-fault tests.
- Packaged application launch tests on Windows/macOS/Linux, including keyboard accessibility and native quit.
- Dependency/toolchain update candidates and full dev-dependency audit.

### Per release — required on the tag commit

1. Reuse all PR validation for the immutable commit.
2. Build from a clean exported tree; assert no source mutation.
3. Verify version/commit across runtime and every platform metadata format.
4. Smoke-test archives/installers and inspect embedded frontend/assets/architecture.
5. Generate SBOM and provenance; sign/notarize; verify signatures and Gatekeeper/SmartScreen-facing metadata.
6. Publish a draft, review checksums/SBOM/changelog, then promote.

## Exit criteria for the delivery system

- A Windows package test cannot fail without failing CI, and `internal/jobs` runs on a required Windows path.
- The exact tag version appears consistently in About, binary metadata, macOS bundle, Windows resources/installers, and Linux packages.
- Release cannot start publishing until reusable validation for the same commit succeeds.
- The supported target list contains no non-building target; unsupported server/mobile/Docker tasks are clearly disabled.
- A clean checkout produces the frontend, desktop binaries, and packages without requiring ignored files or changing tracked files.
- Critical save/lifecycle/approval/workspace-race flows have automated cross-boundary regressions.
- Runtime and build dependency vulnerability checks produce bounded, archived results; unresolved exceptions have owners and expiry dates.
- Official artifacts are signed/notarized where applicable and ship checksums, SBOMs, and provenance attestations.
