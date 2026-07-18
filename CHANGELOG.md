# Changelog

All notable user-visible, security-relevant, migration, deprecation, and support
changes will be documented here. This project has not made a supported public
release; Git history remains the source for older development-only changes.

The format follows Keep a Changelog conventions. Versions will use Semantic
Versioning once public releases begin.

## [Unreleased]

### Security

- Release publication remains intentionally disabled pending native signing,
  notarization, install/launch verification, and owner approval.
- Renderer bridge methods now have a repository-wide exact allowlist test.
- CI and release dependencies are pinned, vulnerability/security scans are
  defined, and release provenance/SBOM source steps are being established.

### Changed

- Large-file claims now describe bounded windows and practical operation limits
  instead of guaranteeing constant cost for arbitrary inputs.
- Server mode and distributable Android packaging fail closed until their
  security and signing designs are complete.

### Removed

- Removed the unused, undocumented root `all.cql` data fixture.

[Unreleased]: https://github.com/RCooLeR/Novera/commits/HEAD
