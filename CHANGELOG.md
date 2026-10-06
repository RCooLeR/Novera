# Changelog

All notable user-visible, security-relevant, migration, deprecation, and support
changes will be documented here. This project has not made a supported public
release; Git history remains the source for older development-only changes.

The format follows Keep a Changelog conventions. Versions will use Semantic
Versioning once public releases begin.

## [Unreleased]

### Security

- Updated DOMPurify to 3.4.16 and source-map-js to 1.2.2 to clear newly reported
  frontend dependency vulnerabilities.
- PostgreSQL connections now reject protocol messages larger than 16 MiB before
  allocating their bodies, using pgx 5.11's protocol limit.
- Malformed XLSX rows in Excelize's disk-backed shared-string lookup return a
  bounded error and release temporary files instead of crashing the application.
  The upstream vulnerability scan finding remains visible pending a complete fix.
- Release publication remains intentionally disabled pending native signing,
  notarization, install/launch verification, and owner approval.
- Renderer bridge methods now have a repository-wide exact allowlist test.
- CI and release dependencies are pinned, vulnerability/security scans are
  defined, and release provenance/SBOM source steps are being established.

### Changed

- Updated Go to 1.27.1, the synchronized Wails stack to beta.26, React to 19.3,
  Vite to 8.3, Vitest to 5, database drivers, compatible transitive dependencies,
  and CI/build tooling. TypeScript remains on 6.0.3 for ESLint compatibility.
- Updated SQLite to 1.60.1 and Monaco to 0.57, including upstream query-binding
  and editor performance fixes. Wails beta.26 adds Windows WebView2 recovery.
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

- CI now checks that Wails Go, frontend, lockfile, and build CLI versions agree.
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
