# Novera project analysis and dependency migration

Reviewed and implemented on **2026-09-20**. This report describes the current
working tree, including the pre-existing migration of application sources into
`src/`. That migration, earlier documentation edits, and deleted `col-review/`
files were preserved. Baseline HEAD was
`78da42fcb2c64d11e01b328c114e0b01aae8d941`. At audit completion, this work was
uncommitted and unpublished; subsequent branch consolidation does not change
the dated validation results below.

## Outcome

The desktop application builds successfully on Windows with Go 1.27.1 and the
synchronized Wails beta.23 stack. All **51 Go packages containing tests** passed
their complete suites, and the frontend passed **247 tests across 42 files**.
The workspace suite also passed with the race detector. Dependencies were
updated against current registry metadata, with two explicit compatibility
holds: TypeScript 6.0.3 and SQLite's exact libc dependency.

Implemented changes address pathological search cost, retained file buffers,
search correctness and cancellation, stale table queries, grid resizing,
PostgreSQL protocol allocation bounds, malformed spreadsheet crashes, and an
iOS build failure being ignored. New dependency capabilities are used where
they improve existing behavior, rather than adding unrelated product features.

This is not a claim that every possible bug has been found or that release
acceptance is complete. The Go vulnerability scanner remains failing on
Excelize; the application has a tested mitigation for its remaining spilled
shared-string panic. Backward regex semantics and native acceptance work remain
open. Android dependency resolution could not be validated by local Gradle.

## Architecture and implementation assessment

| Area | Existing design | Assessment and limits |
| --- | --- | --- |
| Native entry and bridge | `src/main.go` registers Go services, embeds built frontend assets, and owns native close/single-instance wiring. Generated bindings have an explicit method allowlist. | A useful authority boundary. Beta.23 regeneration preserved all 150 methods across 13 services and 81 models. Native lifecycle behavior still needs actual WebView/platform smoke tests. |
| Workspace/filesystem | Go owns path containment, file mutations, stale-save checks, workspace generations, table queries, and watcher integration. | Strong existing adversarial coverage. Snippet retention was fixed. Tree-wide search/diagnostics and table scans still have cancellation/performance limitations. |
| Large-file engine | Separate document, line index, encoding, search, regex validation, staged-edit, replace, plugin, and cache packages. Renderer requests use bounded windows. | Appropriate decomposition for large files. Windowing does not remove full-source indexing, fingerprinting, filtering, or deduplication costs. Search boundary/cancellation regressions were found despite extensive existing tests. |
| Agent and LLM | Backend owns approval lifecycle, tool execution, network policy, provider response limits, cancellation, and workspace transitions. | Existing agent/LLM/provider/network tests passed. No authority expansion was needed. Real provider streams and long-running native sessions were not exercised. |
| Databases | SQL guards, encrypted credentials, profile validation, timeouts, connection limits, and row/cell/result budgets. | Result limits operated after driver parsing. PostgreSQL now also bounds a wire message before allocation. Mock-protocol and full DB suites passed; real remote server matrices remain untested. |
| Terminal and watcher | Native PTY/ConPTY and filesystem services with ownership, cleanup, and event-generation checks. | Full suites passed. This does not prove long-duration behavior under native GUI suspend/resume, filesystem floods, or PTY backpressure. |
| React renderer | Zustand state, lazy Monaco/terminal panes, virtual grids, bridge payload validation, CSP and entry-graph assertions. | Production protections remain intact. Grid/window request lifecycle bugs were fixed. The central store exceeds 4,000 lines, increasing coupling and review cost. |
| Build and release | Root Task facade delegates into `src/`; maintained desktop CI targets Windows/macOS/Linux. Actions and builder images are pinned. Publication is gated. | Windows source build and layout fixtures pass. Added Linux race testing. Release signing, install/launch validation, provenance acceptance, and mobile host readiness remain separate release gates. |

The audit covered manifests and transitive graphs, build/CI scripts, bridge
contracts, renderer state/loading paths, large-file matching and memory
ownership, database parsing, spreadsheet iteration, and existing backend
lifecycle/security suites. It combined source inspection, differential tests,
malicious synthetic fixtures, clean dependency installation, production builds,
and focused benchmarks. It did not involve real user datasets or credentials.

## Dependency changes

### Go and backend libraries

All 41 explicit module requirements were checked against current Go module
metadata. `go.mod` and `go.sum` were tidied and verified.

| Component | Before | After |
| --- | --- | --- |
| Go | 1.27.0 | 1.27.1 |
| Wails Go module and CLI | 3.0.0-beta.15 | 3.0.0-beta.23 |
| MySQL driver | 1.10.0 | 1.10.1 |
| pgx/v5 | 5.10.0 | 5.11.0 |
| modernc SQLite | 1.57.0 | 1.59.0 |
| x/sys | 0.47.0 | 0.48.0 |
| x/text | 0.41.0 | 0.42.0 |
| coder/websocket | 1.8.14 | 1.8.15 |
| go-humanize | 1.0.1 | 1.1.0 |
| go-colorable | 0.1.14 | 0.1.15 |
| mscfb | 1.0.7 | 1.0.8 |
| x/crypto | 0.55.0 | 0.57.0 |
| x/net | 0.58.0 | 0.59.0 |
| x/sync | 0.22.0 | 0.23.0 |
| modernc libc | 1.74.4 | 1.75.7 |
| modernc memory | 1.11.0 | 1.12.1 |

The obsolete `github.com/jchv/go-winloader` requirement was removed by the
updated Wails graph. Unchanged direct dependencies were already current:
go-pty 0.2.3, dpapi 0.5.0, filepath-securejoin 0.7.0, fsnotify 1.10.1,
Excelize 2.11.0, and go-keyring 0.2.8. Remaining explicit indirect requirements
were checked as well. Tool-only modules in Wails' broader graph were not
promoted into application requirements.

**libc compatibility hold:** 1.77.0 exists, but SQLite 1.59.0 explicitly requires
the exact libc version from its own module file, 1.75.7. That requirement is now
documented beside the pin. Arbitrarily taking the newest libc could break
transpiled SQLite code. [SQLite dependency documentation](https://pkg.go.dev/modernc.org/sqlite@v1.59.0)

Go 1.27.1 supplies runtime/compiler and standard-library bug fixes, including
database/sql, HTTP, JSON, and filesystem fixes. This is a patch update within
the existing toolchain family. [Go release history](https://go.dev/doc/devel/release#go1.27.0)

### Frontend libraries and tooling

| Component | Before | After |
| --- | --- | --- |
| Wails JavaScript runtime | 3.0.0-beta.15 | 3.0.0-beta.23 |
| React / React DOM | 19.2.8 | 19.3.0 |
| React types | 19.2.18 | 19.3.0 |
| React DOM types | 19.2.5 | 19.3.0 |
| lucide-react | 1.35.0 | 1.47.0 |
| ESLint | 10.9.1 | 10.11.0 |
| globals | 17.11.0 | 17.12.0 |
| typescript-eslint | 8.68.0 | 8.70.0 |
| Vite | 8.2.2 | 8.3.0 |
| Vitest | 4.1.11 | 5.0.1 |
| DOMPurify override | 3.4.14 | 3.4.15 |
| jsdom, test dependency | absent | 30.1.0 |

All other direct frontend packages already matched current registry releases.
Compatible transitive updates were resolved through `npm update`, followed by a
clean lockfile replay. There are 58 version-changed lockfile records, including
platform variants. Examples include Rolldown 1.2.4 to 1.2.9, PostCSS 8.5.26 to
8.5.28, picomatch 4.0.5 to 4.0.7, brace-expansion 5.0.9 to 5.0.12, and React's
scheduler 0.27.0 to 0.28.0.

**TypeScript compatibility hold:** TypeScript 7.0.2 is available, but the current
typescript-eslint 8.70.0 requires TypeScript `>=4.8.4 <6.1.0`. The project
retains 6.0.3, with no forced peer override or duplicate compiler setup. The
final `npm outdated` result contains only this hold.
[Exact peer metadata](https://registry.npmjs.org/typescript-eslint/8.70.0)

### Build, CI, and Android

| Component | Before | After / decision |
| --- | --- | --- |
| Node LTS CI/documentation pin | 24.20.0 | 24.21.0 |
| GoReleaser | 2.18.0 | 2.18.2 |
| govulncheck | 1.7.0 | 1.8.0 |
| CodeQL action | 4.37.9 | 4.38.1, resolved commit SHA |
| Anchore SBOM action | 0.24.0 | 0.24.2, resolved commit SHA |
| Android Gradle Plugin | 9.3.2 | 9.4.1 |
| Go Docker builder | 1.27.0-bookworm | 1.27.1-bookworm, verified multi-architecture manifest digest |
| Task | 3.53.1 | already current |
| Gradle | 9.7.1 | already current; published checksum matches |
| Zig / macOS SDK archive | 0.16.0 / 26.1 | already current |

The checkout/setup-go/setup-node/upload/download/attest/dependency-review,
Gitleaks, and GoReleaser action releases were checked; their existing releases
were current. AndroidX Activity 1.13.0, AppCompat 1.8.0, Core 1.19.0, WebKit
1.17.0, and Material 1.14.0 were already stable-current. Preview releases were
not selected. The existing Android SDK/target/NDK support decisions remain in
the [Android status document](../src/build/android/README.md).

AGP 9.4 requires Gradle 9.6 or newer and JDK 17; the checked-in Gradle 9.7.1
meets that requirement. Both Google Maven marker and implementation POMs for
9.4.1 were reachable. Local Gradle nevertheless failed to resolve the plugin;
Android compilation is therefore **unverified**, not passed.
[AGP compatibility notes](https://developer.android.com/build/releases/agp-9-4-0-release-notes)

The Node pin stays on the project's LTS family; local frontend validation used
the installed Node 24.20.0/npm 11.19.0. CI is configured for 24.21.0, but that
exact Node patch was not executed locally.
[Node 24.21 release](https://nodejs.org/en/blog/release/v24.21.0)

## Useful capabilities adopted

1. **pgx protocol allocation limit.** Connections use pgx's parsed configuration
   and `MaxProtocolMessageBodyLen = 16 << 20`. A malicious oversized startup,
   metadata, or result message is rejected before allocating its body. A mock
   PostgreSQL server sends only an oversized header to prove early rejection.
   The regression also checks URI fields containing spaces, `+`, and `#`.
   [pgx 5.11 release](https://github.com/jackc/pgx/releases/tag/v5.11.0)
2. **Vitest persistent transform caching.** `fsModuleCache` reuses transforms
   across runs while retaining test isolation. jsdom adds mounted-component
   regressions for the grid and table request lifecycle.
   [Vitest caching](https://vitest.dev/guide/improving-performance#caching-between-reruns)
3. **ESLint native configuration API.** Migrated from deprecated
   `tseslint.config` to ESLint `defineConfig`.
   [Migration reference](https://typescript-eslint.io/packages/typescript-eslint/#migrating-to-defineconfig)
4. **Runtime fixes inherited through upgrades.** Wails beta.16–23 includes
   native memory/resource and aborted-asset-request fixes. Existing platform
   floors and narrow bridge contracts were retained. SQLite's newer library
   contains upstream performance work, but no upstream benchmark percentage is
   claimed as a measured Novera improvement.
   [Wails releases](https://github.com/wailsapp/wails/releases)

## Fixed findings and regression evidence

| Priority | Trigger and previous behavior | Implemented behavior and proof |
| --- | --- | --- |
| P1 | XLSX shared strings larger than the 16 MiB in-memory XML threshold spill to disk; a negative cell index can still panic inside Excelize 2.11.0. | A narrow row-iterator recovery boundary returns a bounded error. Six valid/malformed header/data cases exercise both RAM and actual spill paths and verify temporary-file removal. |
| P1 | PostgreSQL driver could allocate an oversized wire body before application result limits applied. | New pgx limit caps one body at 16 MiB. Header-only oversized-message protocol regression passes. |
| P2 | Short search snippets and diagnostic marker strings referenced the full file string, potentially retaining almost 2 GiB across 2,000 hit-bearing 1 MiB files. | Snippets and marker kinds own their storage. Rune clipping scans only the bounded prefix. Storage-ownership and giant-line tests pass. The 2 GiB value is an upper-bound scenario, not a measured normal workload. |
| P2 | Repeated near-matches made ASCII case-insensitive matching compare a long needle at many positions. | After a small number of failures, an allocation-free rolling hash filters candidates; every hit is verified byte-for-byte. 500 differential cases preserve results. |
| P2 | Whole-word matches beside UTF-8 punctuation could disappear when a delimiter crossed a chunk boundary. | Both adjacent runes are retained. NBSP, euro, and emoji delimiters are tested at multiple chunk sizes/paddings in both directions. |
| P2 | Cancellation during dense search could emit all 4,096 matches and return success after cancellation on the first callback. | Plain and regex searches check cancellation inside matching loops; reverse regex also checks during emission. All four modes now stop after the first callback in this regression. |
| P2 | Forward regex `aa` over repeated `a` bytes could return inconsistent offsets as chunk size changed. | The scan carries the previous accepted match end and avoids unnecessary left overlap. Differential tests compare chunked results with whole-buffer regex matching across patterns, starts, and chunk sizes. |
| P2 | Scrolling far down a table, then shrinking/filtering its dataset, could produce an invalid empty range. Container resizing also left stale viewport dimensions. | Virtual ranges and spacers clamp to valid bounds; ResizeObserver updates the viewport and per-scroll layout measurements are removed. Mounted-component tests cover shrink and resize. |
| P2 | A table's old debounce timer or in-flight response could outlive a file/filter/source change. Fast scroll requests during loading could be lost. | A disposable loader fences obsolete completions and keeps only the latest pending offset for each query generation. Timer and in-flight generation regressions pass. |
| P2 | iOS `xcodebuild ... | xcpretty || true` overwrote compiler failure status, allowing a stale `.app` to be installed. | A small shell helper preserves compiler/formatter failures and builds without a formatter when absent. Five integration cases verify status, intact spaced arguments, single compiler invocation, and no install after failure. |

Frontend work adds **12 tests**, four of which mount React components. Backend
tests use synthetic files and protocol fixtures. No production credentials or
external database instances are required by the new tests.

## Performance evidence

Five runs per direction on Windows/amd64, Intel Core i9-13900KF, Go 1.27.1,
with `-benchtime=100ms -count=5`. Input is a 256 KiB repeated-prefix buffer and
a 1,024-byte near-matching needle. The baseline was the exact pre-edit staged
`fold.go`, Git blob `e8113c5e07edc30f894d2894a65fdc0f91c87b7c`, compiled with the
same current toolchain.

| Direction | Before median | After median | Ratio |
| --- | ---: | ---: | ---: |
| Forward | 86.703650 ms | 0.261263 ms | about 332× |
| Backward | 79.607300 ms | 0.261994 ms | about 304× |

Both have zero median B/op and allocations/op. These are synthetic in-memory
adversarial results, not end-to-end app speed or disk-throughput guarantees.
The five-run evidence supersedes the exploratory single-iteration measurements
reported during implementation. Raw results are retained:
[before](benchmarks/2026-09-20-folded-search-before.txt),
[after](benchmarks/2026-09-20-folded-search-after.txt).

Additional exploratory measurements: ordinary 32 MiB plain search took a
median 4.217 ms with 2,105,368 B/op and three allocations; clipping a 1 MiB line
to 400 runes took 346 ns with 416 B/op and one allocation. These lack a matched
before/after comparison and should be treated as observations only.

Renderer output remains dominated by lazy editor assets: editor API about
2.654 MB, EditorPane about 1.287 MB, and TypeScript worker about 6.926 MB.
Initial application JavaScript is approximately 369 KB plus a 140 KB shared
React chunk. These are emitted byte sizes, not load-time/RSS measurements.
Production entry checks confirm the heavy editor/terminal chunks remain lazy;
bundle-size warnings have not been hidden.

## Verification and practical limits

| Check | Result |
| --- | --- |
| Clean `npm ci --include=dev` | Pass |
| Frontend typecheck / ESLint | Pass |
| Vitest | 247 tests, 42 files, pass |
| npm audit | Zero reported vulnerabilities |
| npm dependency tree | Healthy; only TypeScript hold in outdated report |
| Go module verify / tidy diff | Pass |
| Go vet, app/internal/build scripts | Pass |
| Go complete test suites | 51 packages pass; two of 53 discovered packages have no tests |
| Workspace race-detector suite | Pass on Windows with GCC |
| Wails bindings and bridge allowlist | Regenerated, no generated diff; contract tests pass |
| Windows `task build` | Pass after final source/assets changes; root `bin/Novera.exe` rebuilt |
| Production CSP and lazy entry assertions | Pass |
| iOS compiler failure helper | Five mocked shell integration cases pass under Git Bash |
| govulncheck 1.8.0 | Failing finding retained; see security note below |
| Android Gradle | Plugin resolution failed locally; compile not validated |
| Native macOS/Linux build/launch/package matrix | Not executed on this Windows host |

Some globally installed Go tools and randomly named temporary test executables
stalled before consuming CPU on this host. Such attempts were interrupted and
were not counted as passes. Pinned tools rebuilt under ignored repository
`bin/` ran successfully. Each Go test package was compiled with `go test -c`
to its own stable repository executable and run from its package directory
with a 90-second test timeout; all 51 passed. This avoids conflating a suspended
test process with successful validation. Ordinary hosted-CI `go test` still
needs to pass on clean runners.

Final local command evidence is in ignored `bin/package-tests/summary.txt`,
per-package logs, and `bin/build-audit-final.log`. The frontend was validated
with Node 24.20.0/npm 11.19.0; the backend used Go 1.27.1. The rebuilt executable
is a development-identity, unsigned Windows artifact, not a signed release.
Linux CI now runs `go test -race -timeout 5m . ./internal/...` in addition to its
ordinary tests and production build.

## Security scan disposition

`govulncheck` reports reachable **GO-2026-6452 / CVE-2026-59162** in Excelize
2.11.0. The upstream advisory lists 2.11.0 as fixing the in-memory negative-index
lookup. Source inspection confirms that fix, but the disk-backed lookup still
panics on the equivalent malformed input. The application-level recovery and
cleanup regression mitigate that demonstrated path; they do not establish an
upstream clean bill of health or justify suppressing the scanner.
[Primary advisory](https://github.com/qax-os/excelize/security/advisories/GHSA-fx5j-qcqg-grpf),
[Go vulnerability record](https://pkg.go.dev/vuln/GO-2026-6452)

In-memory malformed indices are returned as empty cells by Excelize, which
swallows its lookup error in streaming iteration. That behavior remains a data
quality limitation. The scanner additionally reports module-only
GO-2026-5932 in `x/crypto/openpgp`; that package is not imported or called by
the application. No findings were suppressed and security gates remain intact.

## Remaining findings and prioritized follow-up

| Priority | Finding | Evidence / next work |
| --- | --- | --- |
| P1, release | Excelize dependency remains scanner-flagged despite app mitigation. | Require an upstream spill-path fix and rerun the malicious fixture and scanner before treating the dependency as clean. |
| P1, release | Wails beta.23 has not passed the full native acceptance matrix. | Test launch, service calls/errors, event rejection, dialogs, external links, close/quit, single instance, and installed artifacts on all maintained OSes. Keep publication frozen. |
| P2, correctness | Backward regex matching still depends on chunk alignment for overlapping candidates. | With 31 `a` bytes, pattern `aa`, and maximum match window 3, chunk size 3 returns `29,26,23,20…`; size 4 returns `28,26,24,22…`. Define reverse-match semantics and add per-pattern checkpoints or a deliberate prefix-scan strategy. A silent whole-prefix latency regression was not introduced. |
| P2, responsiveness | Workspace Search/Diagnostics are synchronous uncancelable tree walks; TableInfo/QueryTable lack native cancellation. | No-match scans can read every eligible file. Disposing a renderer loader suppresses results but cannot stop an already-running native scan. A future change needs backend request identities/cancellation and bridge-contract review. |
| P2, scale | WalkDir materializes/sorts each directory; physical grid spacer heights eventually hit browser limits. | Stress-test extremely wide directories and very large row counts; use segmented scroll mapping if native WebViews reach their height ceiling. |
| P2, integrity | Ordinary viewer/search staleness checks can miss deliberate same-size rewrites with restored timestamps. | Existing write/copy expectations are stronger. Preserve documented limitation; consider filesystem mutation identity without hashing entire files on every navigation. |
| P2, validation | Android build cannot be declared working and mobile host synchronization is incomplete. | Resolve Gradle dependency access, then compile/test the exact pinned configuration and review host/bridge integration. Production Android packaging remains disabled. |
| P3, profiling | Streamed assistant updates may reparse old history; command-palette filtering sorts more results than displayed. | Source-inspection candidates only: profile real sessions before changing state/render architecture. |
| P3, maintenance | Large centralized frontend store and concrete service wiring create broad change surfaces. | Extract cohesive state/actions behind existing interfaces incrementally, with lifecycle and stale-result tests; avoid a wholesale rewrite during the dependency migration. |

Full-source fingerprints, cold line indexing, and exact deduplication are still
proportional work even when memory is bounded. Large-file SQLite/XLSX export,
server mode, MSIX, mobile release signing, and other disabled features were not
silently enabled by dependency updates. Existing release governance, signing,
diagnostics, accessibility, live-provider/database, fuzzing, and clean-machine
installation gates remain described in the project policies.
