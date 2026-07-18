# Security policy

Novera is a privileged local desktop application: it can read and modify files,
start processes, connect to databases and model providers, and retain encrypted
credentials. Please report suspected vulnerabilities privately.

## Supported versions

Novera has not made a supported public release. The release workflow is
intentionally frozen while its security and packaging gates are completed.
Security fixes currently target the default branch only; locally built snapshots
and historical commits do not receive security updates.

When releases begin, this section must be updated with an explicit support
window before the first artifact is published.

## Reporting a vulnerability

Use GitHub's private **Report a vulnerability** form for this repository:

<https://github.com/RCooLeR/Novera/security/advisories/new>

Do not open a public issue containing exploit details, credentials, private
workspace content, model prompts/responses, database contents, or local paths.
Include only the minimum synthetic reproduction necessary, together with:

- the affected commit or build identity shown by Novera;
- operating system and architecture;
- the security boundary crossed and the expected behavior;
- reproduction steps using disposable data; and
- whether you believe active exploitation or credential exposure occurred.

The repository owner must enable and monitor GitHub private vulnerability
reporting before any public release. If the private form is unavailable, do not
publish sensitive details; the absence of a monitored private route is itself a
release blocker.

## Handling expectations

Maintainers should acknowledge a private report within five business days,
preserve the report as confidential, validate it with synthetic data, and agree
on disclosure timing with the reporter. These are response targets, not a
service-level guarantee for this pre-release project.

Security fixes must include a regression test where safe, identify affected
versions, document any credential rotation or data-recovery action, and be
called out explicitly in `CHANGELOG.md`. Release artifacts must not be published
until the release checklist's signing, SBOM, provenance, and verification gates
pass for the exact tag commit.

## Out of scope and safe research

Do not test against systems, accounts, databases, model endpoints, or files you
do not own or have explicit permission to use. Denial-of-service tests must stay
on disposable local environments. Reports based solely on a dependency version
should include evidence that the vulnerable code is reachable in Novera.
