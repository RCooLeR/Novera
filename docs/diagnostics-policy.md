# Diagnostics and support-data policy

Novera is a privileged local application. Diagnostic value never justifies
quietly collecting workspace contents, credentials, database rows, model
transcripts, or command output. Production diagnostics must be local, bounded,
redacted, and user-controlled.

## Allowed default fields

Structured diagnostics may record timestamp, severity, stable error code,
component and operation name, bounded duration/count/byte metrics, cancellation
or terminal state, build identity, OS/runtime versions, and sanitized error
class. Paths may be represented only as a per-install or per-bundle salted hash
plus a non-identifying file type; never write the raw path by default.

Diagnostic values must have length and entry-count caps. Local logs should use
bounded rotation with a documented total byte ceiling and retention period.
Failure to write diagnostics must not overwrite an existing settings, secret,
artifact, recovery, or user-data file.

## Prohibited default fields

Never log secrets or credential references, authorization headers, cookies,
database connection strings, file contents, SQL result rows, prompts/model
responses, tool bodies, terminal/command output, clipboard contents, recovery
journal payloads, or environment-variable values. Error strings from external
providers and child processes must pass a redaction and length boundary before
they enter a diagnostic record.

## Crash and renderer containment

The production application should provide a top-level renderer error boundary,
global error/rejected-promise capture, and a startup failure surface that can
show a sanitized error code and the diagnostic location. Event-schema rejection
must be visible and bounded. Cleanup errors should retain operation/build state
without including the data being cleaned up.

This policy does not claim those runtime facilities are all implemented yet;
their absence remains a release blocker rather than permission to collect raw
logs.

## Opt-in support bundle

A future support-bundle action must be explicit and previewable. Before export,
show the exact files and structured fields, estimated size, redaction rules,
destination, and a cancel option. The bundle should contain only:

- build/OS/runtime identity;
- sanitized bounded diagnostic records;
- configuration *shape* and safe feature flags, never values that may be secret;
- active/recent job state without inputs or outputs; and
- a manifest describing every included file, hash, size, and redaction version.

Workspace paths use a fresh bundle salt so they can be correlated within one
bundle but not across reports. Users must be able to exclude individual entries
and save locally for inspection; Novera must not transmit the bundle
automatically.

Bug reports and security reports must follow `SECURITY.md` and the repository
templates. Maintainers should delete diagnostic attachments when no longer
needed and document any exceptional retention during a coordinated security
investigation.
