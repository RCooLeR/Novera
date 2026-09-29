# Release checklist

This checklist is normative. The failing `release-gate` in
`.github/workflows/release.yml` must remain in place until every required item
is proven for the exact tag commit and the repository owner deliberately
approves publication.

Application commands run in `src/` (frontend commands in `src/frontend/`). The
root Task facade still writes artifacts to root `bin/`; workflow and governance
files remain at the repository root. See the
[repository layout](../README.md#project-layout).

## Governance and identity

- [ ] The copyright owner selected and committed the project license and
  contribution terms; package metadata and every archive agree.
- [ ] `SECURITY.md` names a monitored private route and supported-version window.
- [ ] The curated changelog identifies security fixes, migrations,
  deprecations, breaking changes, and support changes.
- [ ] The tag is immutable `vMAJOR.MINOR.PATCH`, points to the reviewed commit,
  and the runtime/package/archive versions match it.
- [ ] The supported-target matrix in `README.md` matches actual hosted evidence.

## Exact-commit validation

- [ ] Required CI and security workflows passed for the tag commit, not merely
  for another branch commit.
- [ ] Go module verification/tidiness, tests, vet, vulnerability scan, frontend
  audit/typecheck/lint/tests, generated-binding drift, and production CSP/entry
  checks passed from a clean export.
- [ ] Race, fuzz, native WebView accessibility, real DB/provider protocol, and
  package install/launch suites required by the release scope passed.
- [ ] Performance benchmarks were collected on the documented runner and
  reviewed against the accepted allocation/latency/throughput budgets.

## Native artifacts

- [ ] Windows executable/installer has a protected Authenticode signature and
  trusted timestamp; signatures verify before and after archive extraction.
- [ ] macOS app uses Developer ID, hardened runtime, notarization, stapling, and
  passes `codesign`, `spctl`, install, launch, and quit checks.
- [ ] Linux uses the selected signed distribution/package strategy and package
  dependency metadata is install-tested on each supported distribution.
- [ ] Every installer/archive is inspected for architecture, runtime version,
  executable identity, metadata, license/notices, forbidden debug settings, and
  unexpected files.

## Supply-chain evidence

- [ ] Each shipped artifact has SHA-256 checksums, an artifact-specific
  CycloneDX or SPDX SBOM, and GitHub/SLSA provenance bound to that artifact.
- [ ] Checksums and evidence are independently signed or covered by a verified
  attestation; verification instructions are included in release notes.
- [ ] Action commits, builder image digests, downloaded tool checksums, and
  exact Go/Node/Task/Wails versions are recorded.
- [ ] Signing identities and release environments require protected approval;
  untrusted pull requests cannot access secrets or write permissions.
- [ ] A clean-machine smoke test downloads the candidate exactly as a user
  would, verifies it, installs/extracts it, launches it, checks About identity,
  and uninstalls it where applicable.

## Publication and rollback

- [ ] Two reviewers checked the artifact manifest and evidence; one is the
  release owner.
- [ ] Recovery/rollback instructions, credential-rotation guidance, and known
  limitations are in the release notes.
- [ ] The release is initially a draft. Publication, `make_latest`, and update
  channels happen only after final verification.
- [ ] A tested revocation/yank/advisory plan names who can respond if an
  artifact, signing identity, or workflow is compromised.
