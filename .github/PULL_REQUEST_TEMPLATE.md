## Summary

Describe the problem, the bounded change, and its observable result.

## Security and authority

List changes to filesystem/process/network/database/credential authority,
renderer bridge methods, persistence formats, approvals, or supported targets.
Write "none" only after checking each category.

## Validation

- [ ] Added or updated normal and adversarial tests.
- [ ] Ran `go test . ./internal/...` and `go vet . ./internal/...`, or documented an environment blocker.
- [ ] Ran `npm audit --audit-level=moderate`, typecheck, lint, tests, and production frontend build.
- [ ] Regenerated bindings after bound Go changes and reviewed the exact bridge diff.
- [ ] Verified the build did not mutate tracked dependency or generated files.
- [ ] Used synthetic fixtures and removed credentials, local data, logs, and generated outputs.
- [ ] Updated `CHANGELOG.md` and relevant documentation for user-visible/security/migration/support changes.

## Release impact

- [ ] Not release-affecting.
- [ ] Release-affecting; completed the applicable items in `docs/release-checklist.md` and retained the release freeze.
