# Changelog

All notable user-visible, security-relevant, migration, deprecation, and support
changes will be documented here. This project has not made a supported public
release; Git history remains the source for older development-only changes.

The format follows Keep a Changelog conventions. Versions will use Semantic
Versioning once public releases begin.

## [Unreleased]

### Security

- Updated DOMPurify to 3.4.17 and routed Monaco's embedded sanitizer import to
  the patched package in both development and production. Production builds
  reject the old embedded copy; regression tests cover documentation markup,
  dangerous links, and the upstream XHTML SVG attribute fix.
- Updated Go to 1.27.3 across the module, workflows, and Docker image for the
  upstream crypto/x509 certificate-trust fix.
- Updated the synchronized Wails Go runtime, frontend runtime, and CLI to
  beta.31 for the upstream macOS child-frame control-channel, Windows
  notification interpolation, CLI template argument, and server-mode
  request-validation fixes.
- Updated Go to 1.27.2 across the module, workflows, and Docker image to fix
  reachable standard-library vulnerabilities; updated the networking modules
  and vulnerability scanner alongside it.
- Updated DOMPurify to 3.4.16 and source-map-js to 1.2.2 to clear newly reported
  frontend dependency vulnerabilities.
- PostgreSQL connections now reject protocol messages larger than 16 MiB before
  allocating their bodies, using pgx 5.11's protocol limit.
- Pinned Excelize to upstream security patch
  `v2.11.1-0.20261003002531-6258dcebc4e2` pending its next stable release,
  covering all 19 upstream advisories published through October 12. This includes
  a row-iteration denial of service triggered by oversized worksheet row numbers
  (GHSA-jw42-f3rr-4cc3), malformed encrypted-file bounds, negative style IDs,
  and disk-backed shared-string bounds.
  Invalid shared-string references consistently produce empty cells in memory
  and on disk; parser panic containment and temporary-file cleanup remain in place.
- Release publication remains intentionally disabled pending native signing,
  notarization, install/launch verification, and owner approval.
- Renderer bridge methods now have a repository-wide exact allowlist test.
- CI and release dependencies are pinned, vulnerability/security scans are
  defined, and release provenance/SBOM source steps are being established.

### Changed

- Updated SQLite to 1.61.0 with its required libc 1.79.0, Lucide React to 1.61.0,
  Zustand to 5.0.16, Vite to 8.3.5, and CodeQL Action to 4.39.1.
- Updated Go to 1.27.2, the synchronized Wails stack to beta.31, React to 19.3,
  Vite to 8.3, Vitest to 5, database drivers, compatible transitive dependencies,
  and CI/build tooling. TypeScript remains on 6.0.3 for ESLint compatibility.
- Updated SQLite to 1.60.1 and Monaco to 0.57, including upstream query-binding
  and editor performance fixes. Wails beta.26 adds Windows WebView2 recovery.
- Refreshed compatible Go and frontend dependencies, Task 3.54, GoReleaser
  2.18.3, Zig 0.17, Gradle 9.8.1, AndroidX Core 1.19.1, and AndroidX WebKit 1.17.1.
  TypeScript stays on the linter-supported line, SQLite keeps its required libc
  version, and the experimental Android host retains NDK 29 pending native
  compiler validation.
- Linux CI now runs the Go race detector; frontend tests use Vitest's persistent
  transform cache and include mounted-component grid/table regressions.
- Application sources, the Go module, frontend, and native build assets now
  live under `src/`. Root Task entry points and root `bin/` outputs are retained;
  direct Go/Wails commands must run from `src/`.
- Large-file claims now describe bounded windows and practical operation limits
  instead of guaranteeing constant cost for arbitrary inputs.
- Server mode and distributable Android packaging fail closed until their
  security and signing designs are complete.

### Fixed

- Synchronized Wails Go, frontend, and build CLI at beta.34 for upstream
  Windows WebView2 COM dispatch, activation/Tab focus, transient-runtime-error,
  and Vite readiness fixes. Native interaction and packaging acceptance remain
  outstanding, and release publication remains frozen.
- Windows CI now checks each required install, audit, validation, and frontend
  build command separately so a later success cannot hide an earlier failure.
- Aligned CI and release CLI versions with both Wails dependency updates so
  the merged Dependabot changes pass the existing version-alignment check.
- CI now checks that Wails Go, frontend, lockfile, and build CLI versions agree,
  and that the Go module, workflows, and Docker image use the same Go version.
- Increased the Go vulnerability job's setup budget to accommodate observed
  slow Ubuntu package downloads while retaining mandatory vulnerability checks.
- Linux and macOS terminals start successfully with their own PTY session and
  process group, while retaining process-group cleanup during shutdown.
- Go CodeQL analysis follows the relocated module and extracts its source;
  secret scanning excludes only reviewed historical test-reference matches.
- Automated TypeScript updates stay within the linter-supported 6.0 patch line
  so grouped tooling updates do not break installation on peer dependencies.
- Case-insensitive search avoids repeated-prefix slowdowns; whole-word search
  preserves matches beside UTF-8 delimiters spanning read chunks.
- Dense forward/backward searches observe cancellation between matches, and
  forward regex search preserves non-overlapping match alignment across chunks.
- Workspace search and diagnostics retain only bounded snippets instead of the
  entire source file behind a short string.
- Tables recover after dataset shrink or container resize; stale timers and
  results cannot replace a newer query, and queued viewport loads are coalesced.
- iOS device builds stop on compiler or output-formatter failure before installing
  an old artifact.

### Removed

- Removed the unused, undocumented root `all.cql` data fixture.

[Unreleased]: https://github.com/RCooLeR/Novera/commits/HEAD
