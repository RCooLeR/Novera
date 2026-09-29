# Changelog

All notable user-visible, security-relevant, migration, deprecation, and support
changes will be documented here. This project has not made a supported public
release; Git history remains the source for older development-only changes.

The format follows Keep a Changelog conventions. Versions will use Semantic
Versioning once public releases begin.

## [Unreleased]

### Security

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

- Updated Go to 1.27.1, the synchronized Wails stack to beta.23, React to 19.3,
  Vite to 8.3, Vitest to 5, database drivers, compatible transitive dependencies,
  and CI/build tooling. TypeScript remains on 6.0.3 for ESLint compatibility.
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
