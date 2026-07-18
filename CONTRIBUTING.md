# Contributing to Novera

Novera is under active pre-release hardening. Small, reviewable changes with
tests are welcome, but public distribution remains frozen.

## Legal status

The copyright owner has not yet selected a repository license or contribution
license. Maintainers must not merge external code until those terms are chosen
and documented. Opening an issue or submitting diagnostic information does not
grant rights to repository code, and contributors must not include third-party
material they are not authorized to provide.

## Development setup

Use the exact tool versions in `README.md`. Install dependencies and run the
canonical checks from the repository root:

```powershell
cd frontend
npm ci
npm audit --audit-level=moderate
npm run typecheck
npm run lint
npm run test
npm run build
cd ..
go mod verify
go mod tidy -diff
go test . ./internal/...
go vet . ./internal/...
```

Windows endpoint security can interfere with temporary Go test executables. Do
not treat a suspended process as a passing run; capture the environment failure
and rerun on a clean host or hosted CI.

For a production source build, install the pinned Wails and Task CLIs from
`README.md`, then run `task build`. Do not use moving `@latest` tool versions.

## Change requirements

- Keep filesystem, process, database, network, credential, and approval checks
  in the Go backend. Renderer checks are usability controls, not authority.
- Add adversarial tests for mutations, bounds, cancellation, identity, and
  stale-result behavior. Use synthetic fixtures only.
- Do not add a renderer-callable Wails method casually. Regenerate bindings and
  deliberately update `bridge_contract_test.go` only after reviewing the new
  authority and its backend validation.
- Do not import Wails runtime APIs into another business package. Extend a
  Novera-owned adapter or update the reviewed import boundary with justification.
- Run `wails3 generate bindings ...` after changing a bound Go signature and
  commit the generated TypeScript changes.
- Keep generated outputs, local datasets, credentials, logs, and IDE files out
  of commits. Never use real customer or personal data as a fixture.
- Update `CHANGELOG.md` for user-visible behavior, security impact, migrations,
  deprecations, and support changes.

## Pull requests

Describe the problem and security impact, keep unrelated changes separate, and
complete the pull-request checklist. A change that affects release, packaging,
Wails, persistence formats, bridge methods, privileges, or supported targets
requires explicit maintainer review of the corresponding policy/checklist.

Use the private process in `SECURITY.md` instead of a public pull request for an
uncoordinated vulnerability fix.
