# Backend, security, data-integrity, and reliability review

Review date: 2026-07-16
Scope: Go/Wails service layer, agent runtime, workspace and data tools, database and secret handling, Git/watcher/jobs/terminal infrastructure, and internal/bigfile.
Review type: static review plus targeted Go tests, race tests, and vet. Product code was not changed.

## Executive summary

The backend has a substantial amount of thoughtful defensive work: workspace paths are generally contained, LLM redirects are constrained, database queries run behind a read-only guard and read-only transaction, secret values are not directly exposed by the nominal secret facade, several Big File plugins use exclusive outputs and bounded streaming, atomic editor writes fsync their temporary file, and the codebase has unusually broad Big File unit coverage.

Those protections are undermined by several cross-layer gaps:

- Secret references are caller-selected ambient capabilities. A renderer that can list refs can bind any ref to an attacker-controlled LLM or database endpoint and make the Go layer disclose the secret.
- Multiple transformation paths open the input and then truncate a caller-selected output without proving the two files differ. Choosing the source as the output destroys it.
- Big File automatically trusts and replays a predictable adjacent .qrp file when a file is opened. The sidecar is not authenticated or sufficiently validated and recovery errors are discarded.
- Wails binds exported methods that comments describe as internal-only. The bound workspace raw writer and jobs producer API bypass optimistic revisions, approval semantics, and job lifecycle integrity.
- Several operations advertise bounded/streaming behavior but allocate the full provider response, command output, SQL line, INSERT statement, query cell, rollback source, or filtered table result before applying a cap.
- Cancellation and workspace switching are not consistently treated as lifecycle barriers. Old agent work, Git commands, artifact writes, table scans, and Big File session operations can cross into a new logical session.

### Finding count

| Severity | Count |
|---|---:|
| Critical | 3 |
| High | 30 |
| Medium | 9 |
| Low | 0 |
| Total | 42 |

Severity reflects plausible impact in this local desktop application, not just remote exploitability. “Confirmed defect” means the described execution path follows directly from the implementation. “Risk / design gap” means exploitation depends on a renderer compromise, malicious local/provider input, a crash window, or concurrent calls that the current API permits.

## Critical findings

### BE-001 — Caller-selected secret references create a credential-exfiltration confused deputy

- Severity: Critical
- Confidence: High
- Classification: Confirmed security-boundary defect
- Evidence:
  - secret_service.go:5-8 says the frontend has no value getter, but secret_service.go:22-23 exposes every stored reference.
  - internal/settings/settings.go:55-63 and internal/settings/settings.go:278-303 accept and persist an arbitrary LLM BaseURL and APIKeyRef.
  - internal/llm/llm.go:98-110 resolves that arbitrary reference; internal/llm/llm.go:217-227 sends its value as a Bearer credential.
  - internal/agent/agent.go:519-525 resolves the same arbitrary reference; internal/agent/agent.go:1654-1672 sends it to the configured endpoint.
  - internal/db/db.go:177-219 accepts caller-controlled profile IDs and SecretRef values when Password is blank; internal/db/db.go:469-475 resolves that ref and internal/db/db.go:556-625 uses it as a password for the caller-selected host.
- Impact / reproduction:
  1. Obtain a ref from ListKeys, such as a database credential or provider key.
  2. Save LLM settings with that ref and an attacker-controlled HTTPS BaseURL, then call ListModels/Send/Agent.Start; or save a database profile whose SecretRef is that ref and whose host is attacker-controlled.
  3. The Go process retrieves the secret and sends it to the selected peer. The absence of a direct getter therefore does not prevent reading the secret indirectly.
  4. The same API also permits overwriting or deleting another subsystem’s secret because SetKey/DeleteKey accept arbitrary refs.
- Recommendation:
  - Treat a secret handle as a typed, server-owned capability: purpose, owner/profile ID, provider, and allowed destination must be immutable metadata.
  - Never accept an existing SecretRef from the renderer for database profiles. Resolve profile ID to its server-side ref.
  - Namespace and validate LLM refs server-side and bind each ref to an approved provider origin.
  - Require explicit user confirmation when changing the endpoint attached to a credential.
  - Add adversarial tests proving a DB ref cannot be used by LLM services and an LLM ref cannot be attached to another host.

### BE-002 — Big File automatically replays an unauthenticated adjacent .qrp file

- Severity: Critical
- Confidence: High
- Classification: Confirmed integrity and resource-exhaustion defect
- Evidence:
  - internal/bigfile/fileservice.go:111-118 calls recoverInPlace(path) before every open.
  - internal/bigfile/fileservice_edit.go:104-110 derives the predictable path source + ".qrp", invokes recovery, and discards both its result and error.
  - internal/bigfile/inplace/inplace.go:121-154 opens the sidecar and writes every decoded entry directly into the source file.
  - internal/bigfile/inplace/inplace.go:246-286 trusts a sidecar-provided entry count, offsets, and int64 lengths; it allocates make([]byte, n) without a total-size limit and never validates the encoded fileSize against the current source.
- Impact / reproduction:
  - A downloaded/unpacked pair such as dump.sql plus dump.sql.qrp is sufficient. Opening dump.sql causes Novera to mutate it before presenting it.
  - A crafted entry can overwrite arbitrary offsets within that source. Multiple valid early entries followed by an invalid one can partially modify the source; the error is ignored and the modified document is then opened.
  - A huge entry length can force an enormous allocation before io.ReadFull fails, producing an out-of-memory crash.
  - A stale legitimate sidecar can be applied to a different version of the source because source identity, size, hash, and entry ranges are not verified.
- Recommendation:
  - Do not auto-replay a sidecar solely because an adjacent predictable filename exists.
  - Authenticate the journal (random operation ID plus MAC or protected manifest), bind it to canonical source identity, size, mtime and preferably a strong hash, and validate count, total bytes, offsets, non-overlap, and EOF before opening the source for write.
  - Parse into a strictly bounded representation first, then verify all original bytes or a source digest before any mutation.
  - Surface recovery to the user and fail closed; never discard recovery errors.
  - Add malicious-sidecar, stale-source, negative/overflow offset, huge-length, partial-entry, and interrupted-replay tests.

### BE-003 — Workspace data transforms can truncate their own input

- Severity: Critical
- Confidence: High
- Classification: Confirmed deterministic data-loss bug
- Evidence:
  - internal/workspace/workspace.go:1002-1018 resolves and creates the output parent but does not compare the input and output identities.
  - Convert CSV to SQL opens the input and then calls os.Create(output) at internal/workspace/workspace.go:720-743.
  - Dump transform does the same at internal/workspace/workspace.go:857-879.
  - CSV projection and add-column do the same at internal/workspace/workspace.go:905-954.
  - Dump-to-CSV does the same at internal/workspace/workspace.go:971-999.
  - Byte-range extraction calls os.Create at internal/workspace/workspace.go:1021-1041.
- Impact / reproduction:
  - Call ProjectCsv("data.csv", "data.csv", ...), AddCsvColumn with the same paths, or TransformDump("dump.sql", "dump.sql", ...).
  - The input handle is opened first, then os.Create truncates the same file to zero bytes. The transform can return success with empty/partial output, permanently destroying the source.
  - String inequality is not sufficient: relative aliases, symlinks, hardlinks, case aliases on Windows, and equivalent canonical paths can identify the same file.
- Recommendation:
  - Centralize source/output validation and compare canonical file identities with os.SameFile when both exist, plus normalized absolute paths for new outputs.
  - Reject source = destination unless an operation explicitly implements safe in-place semantics.
  - Write to a same-directory temporary file and atomically replace only after a complete successful transform.
  - Add same-string, case-alias, symlink, hardlink, and output-error regression tests for every transform facade.

## High-severity findings

### BE-004 — Bound service surfaces expose methods documented as internal-only

- Severity: High
- Confidence: High
- Classification: Confirmed privilege-boundary / API-design defect
- Evidence:
  - main.go:79-92 registers the full workspace and jobs service values with Wails.
  - internal/workspace/workspace.go:1842-1875 documents ReadRaw/WriteRaw as rollback/internal helpers that bypass encoding and optimistic revision handling.
  - frontend/bindings/novera/internal/workspace/service.ts:205-211 and :286-292 expose both methods to renderer JavaScript.
  - internal/jobs/jobs.go:74 labels Start/Append/Finish “producer API (not bound to the frontend),” but frontend/bindings/novera/internal/jobs/service.ts:16-21, :35-41, and :72-77 bind Append, Finish, and Start.
  - internal/jobs/jobs.go:109-116 lets Finish mark a live job terminal and clears its cancellation handle.
- Impact / reproduction:
  - Renderer code can call WriteRaw directly, overwriting any contained workspace file without an editor revision check and without an agent approval/audit record.
  - Renderer code can call Finish on predictable job IDs, clearing cancellation while the actual operation continues, inject unbounded log strings with Append, and fabricate producer state.
  - All agent approval gates are workflow controls only; they are not an authorization boundary while equivalent direct mutation services remain bound.
- Recommendation:
  - Bind narrow frontend facades, not implementation service types.
  - Move raw I/O and producer methods to unexported collaborators or separate unregistered types.
  - If approval is intended as a security boundary, enforce a server-side operation capability/approval token in the mutation service itself.
  - Add a generated-binding allowlist test that fails if an internal method becomes callable.

### BE-005 — Agent mode sends API keys over plaintext remote HTTP

- Severity: High
- Confidence: High
- Classification: Confirmed credential-disclosure defect
- Evidence:
  - internal/llm/llm.go:153-155 and :195-198 correctly reject a remote HTTP endpoint when a key is present.
  - internal/agent/agent.go:321-327 validates only the URL shape/SSRF literal.
  - internal/agent/agent.go:519-525 resolves the key and internal/agent/agent.go:1654-1672 sends Authorization: Bearer without calling unsafeKeyTransport.
- Impact / reproduction: configure a custom remote http:// provider with a stored key and start Agent mode. The key, prompt, workspace excerpts, tool results, and model response travel without transport authentication or confidentiality.
- Recommendation: share one provider request builder and enforce HTTPS-or-loopback both before a run and immediately before each request. Reject scheme downgrades and add parity tests covering Ask, model listing, and Agent mode.

### BE-006 — Endpoint validation is bypassable through DNS resolution/rebinding

- Severity: High
- Confidence: High
- Classification: Risk / SSRF design gap
- Evidence:
  - internal/netsafe/netsafe.go:22-47 rejects only a literal link-local IP or one hard-coded metadata hostname.
  - Hostnames are not resolved and the selected dial address is not checked.
  - internal/netsafe/netsafe.go:50-61 compares redirect URL hosts, not resolved destinations.
  - This validator is used by background LLM and agent provider calls as well as the HTTP tool.
- Impact / reproduction: an attacker-controlled hostname can resolve to 169.254.169.254, another link-local address, loopback, or an internal service after validation. DNS rebinding can change the answer between requests while the URL host remains unchanged.
- Recommendation: enforce policy in a custom Transport.DialContext: resolve all addresses, reject forbidden classes for each dial, and pin/validate redirects. Preserve local-provider support through an explicit loopback/local allowlist rather than allowing arbitrary private targets silently.

### BE-007 — SQL guard can be bypassed with quoted dangerous function identifiers

- Severity: High
- Confidence: High
- Classification: Confirmed SQL safety-control bypass
- Evidence:
  - internal/sqlguard/guard.go:271-309 discards everything inside double-quoted, backtick-quoted, and bracket-quoted identifiers.
  - internal/sqlguard/guard.go:503-535 blocks dangerous functions only when their unquoted token is marked as called.
  - PostgreSQL accepts a quoted exact-lowercase function name, for example SELECT "pg_read_file"('/etc/passwd') or SELECT "pg_sleep"(60).
- Impact / reproduction: the guard sees SELECT and the call punctuation but never emits pg_read_file/pg_sleep as a token, so it can accept a server-file read or denial-of-service call that the explicit blocklist intends to reject. A read-only transaction does not prevent file reads, sleep, network extensions, or other side-effecting functions.
- Recommendation:
  - Tokenize quoted identifiers as identifiers while still excluding string literals.
  - Prefer an engine-aware SQL parser/AST and permit only a positive subset of expressions/functions.
  - Enforce a statement timeout and least-privilege database role as independent controls.
  - Add quoted, schema-qualified, Unicode/case, comment-separated, and nested-call tests per engine.

### BE-008 — Agent raw-response debug logging is always on, sensitive, and unbounded over time

- Severity: High
- Confidence: High
- Classification: Confirmed confidentiality and disk-exhaustion defect
- Evidence:
  - internal/agent/debug.go:12-32 retains up to 5 MiB of each raw provider response.
  - internal/agent/debug.go:40-69 opens agent-debug.jsonl in append mode for every entry with no opt-in, rotation, total-size cap, or retention.
  - internal/agent/agent.go:1673-1685 records every success and failure; :1699-1723 writes RawResponse.
- Impact / reproduction: model responses and tool-call arguments can contain database rows, file content, commands, tokens copied into prompts, or other user data. Long sessions append indefinitely; at the configured step budget, one run can generate many gigabytes.
- Recommendation: disable raw bodies in production by default; make diagnostics explicit and time-limited; redact/drop tool arguments and content; rotate by total bytes and age; expose a clear-log action; document the location and sensitivity.

### BE-009 — Agent completion responses are read without a byte limit

- Severity: High
- Confidence: High
- Classification: Confirmed resource-exhaustion defect
- Evidence: internal/agent/agent.go:1679-1685 calls io.ReadAll(resp.Body) before the 5 MiB debug truncation or JSON parsing. Request timeouts can be very long.
- Impact / reproduction: a buggy or malicious provider returns an indefinitely large 2xx body. The process accumulates it entirely in RAM and can be terminated by OOM.
- Recommendation: use a hard maximum through LimitReader plus one byte to detect overflow, reject oversized responses, and preferably stream-decode the JSON. Bound request and response sizes separately.

### BE-010 — Cancel removes the active-run barrier before the old run has stopped

- Severity: High
- Confidence: High
- Classification: Confirmed concurrency/lifecycle defect
- Evidence:
  - internal/agent/agent.go:333-340 treats a non-empty cancels map as the one-run invariant.
  - internal/agent/agent.go:403-410 deletes the map entry before signaling cancellation; ResetConversation clears all entries at :360-373.
  - Normal cleanup does not delete the entry until the run defer at :492-509.
  - Approval channels are keyed only by provider callID and overwritten at internal/agent/agent.go:848-852.
  - Only command and HTTP dispatch receive the run context at :787-799; other potentially long tools use context-free closures.
- Impact / reproduction: call Cancel and immediately Start. The new run is admitted while the old goroutine may still be performing file/data/DB work. Provider-reused call IDs can replace an approval waiter, and old work can mutate the workspace or session after reset/switch.
- Recommendation: retain the run entry until the run goroutine acknowledges termination; represent cancel-requested separately; scope approvals by runID + callID; reject duplicate keys; pass context through every tool and wait for run shutdown during reset/workspace transition.

### BE-011 — Command execution allocates all output, can leave descendants alive, and records failures as success

- Severity: High
- Confidence: High
- Classification: Confirmed resource, process-lifecycle, and audit-integrity defect
- Evidence:
  - internal/agent/agent.go:1787-1814 uses cmd /c or sh -c and CombinedOutput, then clips only after the complete byte slice has been allocated.
  - process_windows.go configures window visibility but no Job Object; the non-Windows implementation establishes no process group.
  - internal/agent/agent.go:1815-1828 returns timeout, cancellation, non-zero exit, and launch failure as text with a nil error.
  - internal/agent/agent.go:800-808 therefore records status “ok” for those failures.
- Impact / reproduction:
  - A command that prints gigabytes can exhaust memory despite MaxToolOutputChars.
  - A shell can launch a background/grandchild process that survives context cancellation and continues modifying files.
  - Audit history falsely marks failed, timed-out, or canceled commands as successful.
- Recommendation: stream stdout/stderr into a bounded ring/spool, kill an OS process group/Windows Job Object, wait for the full tree, and return typed errors/outcomes so audit status remains truthful.

### BE-012 — The rollback size cap is applied after reading the entire file

- Severity: High
- Confidence: High
- Classification: Confirmed resource-exhaustion defect
- Evidence:
  - internal/agent/rollback.go:12-18 declares an 8 MiB snapshot cap.
  - internal/agent/rollback.go:55-68 calls ws.ReadRaw before checking len(data).
  - internal/workspace/workspace.go:1845-1857 implements ReadRaw with os.ReadFile.
- Impact / reproduction: approving deletion or overwrite of a multi-gigabyte file loads the whole file into memory and only then labels it too large to undo.
- Recommendation: stat first, open and bounded-read only eligible regular files, and use a disk-backed durable snapshot for larger approved mutations when undo is promised.

### BE-013 — Incomplete rollback entries are executed and reported as fully restored

- Severity: High
- Confidence: High
- Classification: Confirmed data-integrity and user-trust defect
- Evidence:
  - internal/agent/rollback.go:55-71 skips unreadable/oversized paths, while RollbackEntry retains an incomplete flag.
  - internal/agent/rollback.go:104-140 restores only available snapshots and never rejects or reports incomplete restoration.
  - internal/agent/agent.go:1521-1530 always returns “restored” followed by the advertised file list.
  - delete_file promises recursive deletion is undoable at internal/agent/agent.go:1489-1503, but ReadRaw cannot snapshot a directory tree.
- Impact / reproduction:
  - Delete a directory or a file larger than 8 MiB and then invoke rollback. It can return success after restoring nothing.
  - A move over a large destination may restore the source but not the overwritten destination while claiming both were restored.
- Recommendation: distinguish fully undoable, partially undoable, and non-undoable entries; refuse destructive mutation when the approved UX promised undo but a snapshot cannot be completed; store skipped paths/reasons; report per-path restore results; snapshot directory trees durably.

### BE-014 — Transformation outputs overwrite valid files non-atomically

- Severity: High
- Confidence: High
- Classification: Confirmed data-loss defect
- Evidence:
  - Workspace transforms use os.Create at internal/workspace/workspace.go:734, :871, :920, :946, :985, and :1030.
  - internal/workspace/workspace.go:1034-1037 suppresses io.EOF from CopyN without proving the requested range was fully copied.
  - shell_service.go:32-48 uses direct os.WriteFile for a user-selected existing file.
- Impact / reproduction: a malformed source, cancellation, disk-full condition, source shrink, or write error destroys the prior output and leaves a partial/truncated file. Range extraction can report success after copying fewer bytes than planned.
- Recommendation: write to an exclusive same-directory temporary file, flush/fsync, verify expected counts, then atomically replace according to an explicit overwrite policy. Preserve the previous output on every error and fsync the directory where crash durability matters.

### BE-015 — CSV readers suppress non-EOF read errors and return successful truncated output

- Severity: High
- Confidence: High
- Classification: Confirmed silent-data-loss bug
- Evidence:
  - Schema inference breaks on a non-EOF CSV error and returns partial results without marking Truncated at internal/datatools/datatools.go:123-166.
  - CSV-to-SQL breaks and returns success at internal/datatools/sql.go:119-166.
  - ProjectCSV and AddCSVColumn do the same at internal/datatools/transform.go:142-163 and :185-199.
- Impact / reproduction: inject an underlying reader error after several records (or encounter a real mid-stream filesystem/network-volume read fault). Generated SQL/projected CSV ends at that point, the error is discarded, and the workspace facade can register the partial output as a successful artifact. LazyQuotes deliberately tolerates malformed quote syntax, so this finding is specifically about the non-EOF errors the loop receives and ignores.
- Recommendation: return the csv.Reader/I/O error with record/line context. If tolerant mode is a product requirement, make it explicit and return partial=true, skipped/error counts, and a visible warning; never use silent tolerance for file-producing operations.

### BE-016 — Legacy CSV-to-SQL escaping can generate executable MySQL injection

- Severity: High
- Confidence: High
- Classification: Confirmed generated-code vulnerability
- Evidence:
  - internal/datatools/sql.go:29-30 escapes only single quotes.
  - internal/datatools/sql.go:137-152 interpolates each CSV value into generated MySQL-style SQL.
  - MySQL commonly treats backslash as an escape unless NO_BACKSLASH_ESCAPES is enabled. A value containing backslash followed by quote can change how the doubled quote sequence is parsed.
  - The newer Big File CSV SQL plugin has more complete dialect-specific escaping at internal/bigfile/plugins/csv/sql.go:723-774, demonstrating the legacy path is inconsistent.
- Impact / reproduction: an untrusted CSV value such as a backslash/quote sequence followed by SQL syntax can close the generated string under the default MySQL mode when the output is imported. At minimum, valid values round-trip incorrectly; at worst, importing the generated file executes injected statements.
- Recommendation: remove or route the legacy generator through the tested dialect-aware implementation. Escape backslashes before quotes for MySQL or emit safe hex literals, state the target dialect/sql_mode, and add import-roundtrip tests with quotes, backslashes, NUL/control bytes, multibyte text, and payload-like values.

### BE-017 — Regex dump transforms rewrite data/comments and interpolate unsafe replacement text

- Severity: High
- Confidence: High
- Classification: Confirmed corruption/injection defect
- Evidence:
  - internal/datatools/transform.go:31-44 defines global line regexes for DEFINER, ENGINE, CHARSET, COLLATE, and AUTO_INCREMENT.
  - internal/datatools/transform.go:64-86 runs them on every line without tracking SQL strings/comments/statements.
  - Engine, Charset, Collation, and ToDatabase are inserted into regexp replacement strings at :74-85; dollar signs have replacement-template semantics and identifiers are not validated.
- Impact / reproduction:
  - A string literal or comment containing ENGINE=MyISAM is rewritten even though it is data, not DDL.
  - Crafted replacement names containing dollar/capture syntax produce unexpected expansion; SQL punctuation can corrupt or inject the output.
- Recommendation: tokenize/parse SQL and modify only recognized syntactic clauses. Validate engine/charset/collation against supported values; quote database identifiers by dialect; use literal replacement functions instead of replacement templates.

### BE-018 — Dump extraction assumes each table occupies one contiguous region

- Severity: High
- Confidence: High
- Classification: Confirmed algorithmic limitation presented as general extraction
- Evidence:
  - internal/datatools/sql.go:176-206 plans one range from each table’s first CREATE/INSERT offset to the next table’s first offset.
  - internal/bigfile/plugins/sql/extract/plan.go:102-144 uses the same contiguous-region model.
- Impact / reproduction: many standard dumps create several tables first and emit COPY/INSERT data later. Extracting table A can omit its later data; extracting table B can include unrelated statements/data belonging to other tables. Fixtures that keep DDL and data adjacent do not expose this.
- Recommendation: index statement/block ownership and allow multiple ordered spans per table. Test all-DDL-then-all-data PostgreSQL dumps, repeated INSERT blocks, schema-qualified duplicates, routines/triggers, and interleaved comments/settings.

### BE-019 — Dump-to-CSV can merge schemas and accept truncated/corrupt COPY blocks

- Severity: High
- Confidence: High
- Classification: Confirmed correctness defect
- Evidence:
  - internal/datatools/copy.go:145-150 matches either exact name or the last dotted segment, even when the caller supplied a qualified name.
  - internal/datatools/copy.go:63-128 returns success at EOF even while inCopy or collecting an incomplete INSERT.
  - internal/datatools/copy.go:165-197 does not implement PostgreSQL COPY octal/hex byte escapes and drops the backslash for unknown escapes.
- Impact / reproduction:
  - Request public.users from a dump that also contains audit.users; both blocks match and rows are appended to one CSV.
  - Truncate a COPY block before the terminating backslash-dot line; the partial CSV is returned as success.
  - Values using supported PostgreSQL octal/hex escapes are decoded incorrectly.
- Recommendation: require exact match for qualified input; reject ambiguous bare names; require a complete terminator/statement; implement the documented COPY text decoder or reuse a proven parser; return nulls distinctly from empty strings.

### BE-020 — “Streaming” SQL operations allocate unbounded logical lines/statements

- Severity: High
- Confidence: High
- Classification: Confirmed resource-exhaustion defect
- Evidence:
  - internal/datatools/datatools.go:198-259 uses bufio.Reader.ReadString(newline).
  - internal/datatools/transform.go:47-101 does the same.
  - internal/datatools/copy.go:28-40 and :63-123 retain an entire multi-line INSERT in strings.Builder before parsing.
- Impact / reproduction: mysqldump commonly emits huge extended INSERT statements on one line. A multi-gigabyte line defeats the “multi-GB streaming” claim and can exhaust memory during analysis, transform, or table export.
- Recommendation: parse bounded chunks with a state machine, stream tuple/block events, establish explicit per-token/per-statement limits, and expose cancellation/progress.

### BE-021 — Database query limits rows but not columns, cells, or total bytes

- Severity: High
- Confidence: High
- Classification: Confirmed resource-exhaustion defect
- Evidence: internal/db/db.go:389-453 caps result rows but scans every column and converts every value to a full string; internal/db/db.go:652-663 converts byte slices without a size cap.
- Impact / reproduction: a permitted SELECT can return one very large text/BLOB cell (or an extreme column count) and exhaust Go memory or the Wails bridge despite the 5,000-row cap.
- Recommendation: cap column count, per-cell bytes, and total serialized response bytes; stop scanning at the total budget and mark truncation precisely; consider database-side length projection and driver-specific limits.

### BE-022 — Default remote database TLS encrypts without authenticating the server

- Severity: High
- Confidence: High
- Classification: Risk / transport-security design gap
- Evidence:
  - internal/db/db.go:568-586 defaults remote PostgreSQL to sslmode=require, which encrypts but does not provide full hostname verification in the normal PostgreSQL semantics.
  - internal/db/db.go:605-625 explicitly defaults remote MySQL to skip-verify.
  - Existing tests intentionally lock in those defaults at internal/db/db_test.go:30-38 and :59-67.
- Impact / reproduction: an active network attacker can impersonate the database server and collect credentials or alter “read-only” query results. Comments label this “secure-by-default,” which overstates the guarantee.
- Recommendation: default to verify-full / certificate verification with system roots and server name. Make insecure TLS an explicit advanced opt-out with a persistent warning and fingerprint/CA options.

### BE-023 — Profile and secret writes are not transactional with in-memory state

- Severity: High
- Confidence: High
- Classification: Confirmed state-divergence defect
- Evidence:
  - internal/db/db.go:177-219 writes a secret first, mutates s.profiles, and only then persists profiles.
  - internal/db/db.go:223-247 mutates the profile slice before persist; a persist error leaves runtime state different from disk.
  - internal/secret/secret.go:103-121 and :132-137 mutate the in-memory map before persist and do not roll back on failure.
- Impact / reproduction: make the config directory unwritable/full during SaveProfile, DeleteProfile, Set, or Delete. The call returns an error but memory and disk can disagree, a new secret can be orphaned, and a later unrelated successful save can unexpectedly persist the failed mutation.
- Recommendation: use copy-on-write state, persist the candidate atomically, then swap memory. Coordinate profile+secret updates with compensating rollback or a single durable transaction journal. Return not-found for deletion and test injected write/rename failures at every phase.

### BE-024 — A corrupt keyring master key is silently replaced

- Severity: High
- Confidence: High
- Classification: Confirmed secret-recoverability defect on non-Windows platforms
- Evidence: internal/secret/secret_other.go:41-61 generates and stores a new key when the existing keyring value is malformed or not 32 bytes.
- Impact / reproduction: corrupt/truncate the keyring entry while encrypted secrets remain on disk. The next access overwrites the only master-key slot, permanently making all existing ciphertext undecryptable.
- Recommendation: fail closed and preserve the corrupt key. Offer an explicit backup/recovery/reset flow that warns that reset invalidates stored secrets; never overwrite key material as an automatic error-recovery action.

### BE-025 — Corrupt artifact metadata is treated as empty and overwritten

- Severity: High
- Confidence: High
- Classification: Confirmed metadata-loss defect
- Evidence:
  - internal/artifacts/artifacts.go:71-87 returns an empty registry on any JSON unmarshal failure.
  - The next Create/Archive/Delete path saves that empty/new list at internal/artifacts/artifacts.go:90-113 and :180-240.
- Impact / reproduction: truncate .novera/artifacts.json, open the artifacts UI, then register any artifact. Existing lineage/history is silently discarded rather than recoverable.
- Recommendation: surface a corruption error, preserve/backup the original, block mutation until recovery, and use versioned schema plus atomic writes with a verified last-known-good copy.

### BE-026 — Jobs retention can evict live work and lose its cancellation handle

- Severity: High
- Confidence: High
- Classification: Confirmed lifecycle defect
- Evidence: internal/jobs/jobs.go:78-88 appends a new job and unconditionally evicts the oldest entry whenever the list exceeds 200, regardless of Status.
- Impact / reproduction: start more than 200 concurrent/long-running jobs. An older running job is removed from byID; subsequent Append/Finish become no-ops, CancelJob cannot reach its cancel func, and the underlying work continues untracked.
- Recommendation: never evict running jobs. Apply retention only to terminal entries, reject/queue starts at a concurrency limit, and keep a separate bounded history.

### BE-027 — Workspace switching can commit results from the previous workspace

- Severity: High
- Confidence: High
- Classification: Confirmed cross-workspace race family
- Evidence:
  - Artifact Create captures root for file validation at internal/artifacts/artifacts.go:193-200, then load/save recomputes the current root at :63-113 after locking.
  - TableInfo and filtered table scans execute outside tableMu and install cache results later at internal/workspace/workspace.go:1271-1323 and :1435-1487; cache keys contain relative path/signature but not root/generation.
  - Git methods validate against one s.root call but command execution calls s.root again for cmd.Dir at internal/gitsvc/git.go:258-277 and :322-330.
- Impact / reproduction: start a slow scan/artifact-producing operation/Git action in workspace A and switch to B before its commit point. Metadata can be written to B for a file validated in A, old table rows can repopulate B’s cache (especially for identical rel/size/mtime), or a Git mutation can execute in B.
- Recommendation: capture a workspace handle containing canonical root plus monotonically increasing generation at operation start; pass it through every subordinate call; verify the generation immediately before cache/state/disk commit; cancel and await old-workspace operations during Open.

### BE-028 — Big File session registry is not safe for the concurrent use it advertises

- Severity: High
- Confidence: High
- Classification: Confirmed data race / use-after-close risk
- Evidence:
  - internal/bigfile/session/registry.go:38-43 claims concurrent safety, but Get returns a mutable File pointer at :87-92.
  - File.EditSession and ResetEdits mutate Edit without a lock at :25-35.
  - Reopen drops the registry lock and swaps/closes Doc/Edit at :66-84.
  - Close removes the pointer, then reads cancelIndex and closes Doc at :95-109; StartIndexing overwrites cancelIndex at :112-118.
- Impact / reproduction: concurrent Wails calls such as GetWindow, RefreshFile, CloseFile, edit operations, and indexing can race over Doc/Edit/cancelIndex. One call can use a document another closes; Reopen can install/start a new document after Close removed the entry; documents and indexing goroutines can leak or return inconsistent bytes.
- Recommendation: give each File a lifecycle mutex/state, acquire a lease/reference for every RPC, atomically swap under that lock, cancel and wait for indexing, and close the old document only after readers drain. Add concurrent Close/Reopen/Edit/Window race tests.

### BE-029 — Big File SQL analysis cache survives refresh and close

- Severity: High
- Confidence: High
- Classification: Confirmed stale-offset and memory-leak defect
- Evidence:
  - internal/bigfile/fileservice.go:50-65 stores sqlSummary by file ID.
  - CloseFile and RefreshFile at internal/bigfile/fileservice.go:131-168 do not invalidate it.
  - internal/bigfile/fileservice_sql.go:45-60 stores summaries and :84-105 reuses their byte offsets for extraction.
- Impact / reproduction: analyze a dump, modify/grow/reorder it externally, call RefreshFile, then extract a table. Stale offsets can export the wrong bytes/table or an invalid range. Closing many analyzed files also retains their summaries for the service lifetime.
- Recommendation: invalidate on refresh/close and key by immutable document generation plus source identity/size/mtime/hash. Verify the generation before executing an offset plan.

### BE-030 — Big File direct-output facades can truncate the opened source and leave partial outputs

- Severity: High
- Confidence: High
- Classification: Confirmed data-loss defect
- Evidence:
  - HarvestMatchesViaDialog calls os.Create(dst) at internal/bigfile/fileservice.go:331-382 with no source/output identity check.
  - SQL schema/data exporters use exportRanges, which calls os.Create at internal/bigfile/fileservice_sql.go:327-350.
  - SqlSampleFixtureViaDialog calls os.Create at internal/bigfile/fileservice_sql.go:352-416.
- Impact / reproduction: in a save dialog, select the currently opened source file as the destination. It is truncated before the document reader copies from it. Any later error also leaves a partial output and destroys an existing destination.
- Recommendation: route every facade through one safe-output helper that rejects os.SameFile aliases, writes exclusively to a temp file, fsyncs, and atomically commits. Match the stronger behavior already implemented by exportx/replace/newer plugins.

### BE-031 — Big File bound budgets are not consistently clamped

- Severity: High
- Confidence: High
- Classification: Confirmed resource-exhaustion / API-hardening defect
- Evidence:
  - SearchAll accepts any positive maxHits at internal/bigfile/fileservice.go:276-328 and materializes result plus preview slices.
  - GetCsvGrid accepts arbitrary maxBytes at internal/bigfile/fileservice_csv.go:185-220.
  - GetEditWindow/GetDiffWindow accept arbitrary maxBytes at internal/bigfile/fileservice_edit.go:125-165 and :221-284.
  - The edited PieceTable path preallocates end-start bytes at internal/bigfile/manualedit/piece_table.go:270-301.
  - GetHexWindow has the same caller-controlled pattern at internal/bigfile/fileservice_hex.go:32-50.
- Impact / reproduction: invoke a generated binding with a huge maxHits/maxBytes value. Depending on the path, the service allocates enormous slices, spends the full search timeout collecting millions of previews, or errors only after expensive alignment/work. An edited-view request can preallocate directly from the caller’s range.
- Recommendation: define immutable server-side maxima for every bridge method, validate overflow before arithmetic, cap total serialized bytes, and fuzz boundary/negative/max-int inputs.

### BE-032 — Many full-file Big File operations are synchronous and uncancelable

- Severity: High
- Confidence: High
- Classification: Confirmed availability / UX-contract gap
- Evidence:
  - SqlAnalyze uses context.Background at internal/bigfile/fileservice_sql.go:42-60.
  - SQL extract/split/replace/reshape paths use context.Background at internal/bigfile/fileservice_sql.go:105, :198, :520-526, :556, and :678-693.
  - Several CSV profile/project/convert paths use context.Background at internal/bigfile/fileservice_csv.go:83-386 and :688-736.
  - The cancellable withJob wrapper is used only by a subset of CSV transformations at internal/bigfile/fileservice_csv.go:484-660.
- Impact / reproduction: launch analysis or conversion on a hundreds-of-gigabytes file. CancelJob cannot stop it, and a synchronous Wails RPC remains occupied until completion/error. Workspace/app lifecycle cannot reliably quiesce it.
- Recommendation: put every O(file-size) operation behind the same job manager, pass its context into every plugin, report progress, and clean partial output on cancel. Add cancellation latency tests.

### BE-033 — Replace recovery manifests are not durable state machines and recovery trusts manifest paths

- Severity: High
- Confidence: Medium
- Classification: Risk / crash-consistency and recovery-authority gap
- Evidence:
  - internal/bigfile/replace/file.go:285-301 rewrites manifests directly through a truncating output and closes without fsync/atomic rename.
  - internal/bigfile/replace/file.go:194-222 changes output/source files, mutates phases in memory, and persists only later; a crash can leave disk state ahead of the recorded phase.
  - internal/bigfile/replace/recovery.go:29-39 accepts path fields from JSON without binding them to the manifest location or operation identity.
  - DeleteRecoveryTemp and ResumeRecovery delete/rename the manifest-provided TempOutput, Output, Source, and Backup paths at internal/bigfile/replace/recovery.go:153-161 and :279-352.
- Impact / reproduction:
  - Power loss between rename/swap and the next manifest write leaves an old phase that cannot distinguish whether a rename occurred.
  - A corrupt or crafted manifest passed into recovery helpers can direct deletion or rename operations at unrelated paths.
  - A torn manifest write can erase the only recovery state.
- Recommendation: write manifests atomically and fsync file+directory; persist and fsync every phase before and after destructive transitions; reconcile actual file identities/hashes on resume; restrict all related paths to an operation-owned directory/name set; authenticate/version manifests; add crash-point fault-injection tests.

## Medium-severity findings

### BE-034 — Artifact creation mutates persistent metadata without an agent approval

- Severity: Medium
- Confidence: High
- Classification: Confirmed approval-policy inconsistency
- Evidence:
  - internal/agent/agent.go:776-785 gates only tools marked mutating/gated.
  - create_artifact at internal/agent/agent.go:1533-1555 writes .novera/artifacts.json through CreateArtifact but has neither flag.
- Impact / reproduction: a model can create/update artifact registry entries without the approval required for other persistent workspace changes. This can overwrite title, note, tool, and lineage for an existing path.
- Recommendation: mark it mutating, or explicitly classify artifact metadata as non-authoritative/ephemeral and separate it from workspace files. Ensure the system prompt accurately describes the policy.

### BE-035 — Database schema browsing loses schema identity and rejects valid SQLite names

- Severity: Medium
- Confidence: High
- Classification: Confirmed correctness/API-model defect
- Evidence:
  - PostgreSQL ListTables returns only table_name across all non-system schemas at internal/db/db.go:631-639.
  - ListColumns then chooses the first matching search_path schema at internal/db/db.go:339-351.
  - SQLite ListTables exposes arbitrary valid names, but ListColumns rejects spaces, punctuation, non-ASCII, and leading digits through safeIdent at internal/db/db.go:315-385.
- Impact / reproduction: two PostgreSQL schemas containing users are indistinguishable and selecting one can show the other’s columns. A valid SQLite table named order-items or 2026 data appears in the browser but cannot be inspected.
- Recommendation: return schema/catalog as first-class fields and pass schema+table back to column lookup. Use engine-supported parameterized metadata APIs or correct identifier quoting rather than a reduced identifier grammar.

### BE-036 — LLM timeout accepts overflow and impractically large values

- Severity: Medium
- Confidence: High
- Classification: Confirmed validation defect
- Evidence: internal/settings/settings.go:101-109 multiplies caller-controlled RequestTimeoutSec by time.Second without a maximum/overflow check; Save normalizes but does not clamp it at internal/settings/settings.go:278-303.
- Impact / reproduction: a sufficiently large persisted integer overflows time.Duration to a negative or unrelated value, causing immediate or unpredictable deadlines. Smaller but extreme values leave requests effectively unbounded.
- Recommendation: clamp to a documented sane range and use overflow-safe duration construction. Reject invalid settings rather than silently accepting them.

### BE-037 — Watcher shutdown races the event loop and Watch hides failures

- Severity: Medium
- Confidence: High
- Classification: Confirmed concurrency and observability defect
- Evidence:
  - New starts loop() over s.w at internal/watcher/watcher.go:48-59.
  - ServiceShutdown writes s.w = nil at :62-72.
  - loop reads s.w.Events and s.w.Errors without the mutex at :137-153.
  - Watch silently ignores invalid paths and Add errors and returns nil at :103-134.
- Impact / reproduction: concurrent shutdown/event-loop evaluation races on the watcher pointer. If shutdown clears s.w before the newly started goroutine evaluates s.w.Events, dereferencing the nil watcher can panic. Hitting an OS watch limit or passing an invalid path also leaves the UI believing files are monitored.
- Recommendation: pass the immutable watcher pointer into loop, close it, wait with a WaitGroup, then clear state. Return per-path watch status/errors and test shutdown under race detection and simulated Add failure.

### BE-038 — Git command output is unbounded and status failures masquerade as a clean repository

- Severity: Medium
- Confidence: High
- Classification: Confirmed resource and correctness defect
- Evidence:
  - internal/gitsvc/git.go:322-347 uses cmd.Output and an unbounded bytes.Buffer for stderr.
  - Diff reads the complete working file before checking maxDiffBytes at internal/gitsvc/git.go:172-190.
  - UnifiedDiff caps only after the whole git diff has been captured at :208-238.
  - Status uses gitOK for branch/head/status at :83-110; gitOK converts every error/timeout to an empty string at :350-355.
- Impact / reproduction: a huge diff/blob/status output can exhaust memory. If rev-parse succeeds but status times out/fails, Status returns Available=true and “clean,” hiding changes. Commit’s stat check can report “No staged changes” on an error.
- Recommendation: stream/cap stdout and stderr during execution; propagate typed failures; distinguish empty successful output from failure; apply the cap before reading working-tree files.

### BE-039 — Table row indexing disagrees with the CSV parser and result caps ignore bytes

- Severity: Medium
- Confidence: High
- Classification: Confirmed correctness and resource-bound defect
- Evidence:
  - The actual reader uses encoding/csv with LazyQuotes at internal/workspace/workspace.go:1559-1579.
  - scanCSVRows treats any quote byte in normal state as the start of a quoted record at :1326-1403, regardless of field position.
  - Filtered/sorted results retain up to 500,000 normalized rows and all cells at :1435-1487, with no byte budget.
- Impact / reproduction:
  - A lazy-quote row such as abc"def can put the indexer into quoted state and make later newlines disappear from row counts/offsets.
  - One enormous cell or many wide rows can consume extreme memory even below 500,000 rows.
- Recommendation: build offsets with the same parser semantics, preferably using csv.Reader.InputOffset; enforce per-cell and total-result byte limits; spill large sorts/filter indexes to disk.

### BE-040 — ReadFileRange can split encodings and return corrupt text pages

- Severity: Medium
- Confidence: High
- Classification: Confirmed display/data-boundary defect
- Evidence:
  - internal/workspace/workspace.go:1696-1751 reads an arbitrary byte range and converts it directly with string(buf).
  - The API does not carry the encoding that full ReadFile supports (UTF-8 BOM, UTF-16LE/BE, Latin-1).
- Impact / reproduction: request a range beginning/ending in the middle of a UTF-8 sequence or UTF-16 code unit. JSON/UI receives replacement characters or invalidly decoded content; navigating pages can appear to alter/drop characters.
- Recommendation: return raw bytes/base64 plus encoding and use a stateful decoder, or align page boundaries to decoder-safe units with overlap/carry state.

### BE-041 — Atomic workspace writes change permissions and optimistic revision is only advisory

- Severity: Medium
- Confidence: High
- Classification: Confirmed metadata bug plus concurrency gap
- Evidence:
  - internal/workspace/workspace.go:1979-2001 creates a fresh temporary file and renames it over the target without copying the existing mode/ownership or fsyncing the directory.
  - internal/workspace/workspace.go:1807-1815 explicitly checks the revision separately from the later rename.
  - Rename at internal/workspace/workspace.go:1915-1935 has no explicit destination-exists policy; os.Rename replacement behavior differs by platform.
- Impact / reproduction:
  - On Unix, saving an executable script can replace it with a non-executable 0600-style temp file.
  - An external modification between hash check and rename is overwritten despite optimistic concurrency.
  - Rename can silently replace an existing destination on Unix but fail on Windows, creating data-loss and cross-platform inconsistency.
- Recommendation: preserve mode and relevant metadata, fsync the parent directory, use platform-specific compare-and-swap/file-ID checks where feasible, and make overwrite policy explicit and consistent.

### BE-042 — Model listing and streaming can report malformed/truncated responses as success

- Severity: Medium
- Confidence: High
- Classification: Confirmed protocol-handling and resource-bound defect
- Evidence:
  - ListModels decodes an unbounded JSON body/array at internal/llm/llm.go:141-189.
  - Streaming ignores malformed data chunks and chunks with no choices at internal/llm/llm.go:252-275.
  - Clean EOF without a DONE marker still emits done at :276-291.
- Impact / reproduction: a provider can return an enormous model list and exhaust memory. A truncated/error SSE stream can silently drop error chunks and appear successfully complete, leaving the user with an incomplete answer.
- Recommendation: cap body bytes and model count/ID size; parse provider error events; track terminal finish reason/DONE and treat premature EOF or malformed protocol records as failure.

## Additional reliability gaps and consolidation notes

The following related issues are included in the numbered findings above rather than split into additional IDs:

- Missing artifact sources are silently treated as “not stale” at internal/artifacts/artifacts.go:127-139. Introduce an “unverifiable/broken lineage” state.
- FileService has no ServiceShutdown/CloseAll counterpart; internal/bigfile/session/registry.go only closes one ID. Add deterministic shutdown that cancels and awaits indexing/jobs, closes every document, and clears caches.
- withJob cleanup is not deferred at internal/bigfile/jobs.go:41-78. A panic can leave the one-job slot permanently occupied. Use defer/recover for lifecycle cleanup.
- Terminal Start has no session/concurrency quota at internal/terminal/terminal.go:63-93. The PTY read path has useful backpressure, but the bound API can still create an arbitrary number of shells. Add a modest configurable cap and per-session shutdown timeout.
- Agent command and HTTP tool results are clipped by characters after execution; caps must be enforced during acquisition, not only at serialization.
- Workspace assertContainedReal explicitly remains a best-effort TOCTOU mitigation at internal/workspace/workspace.go:1956-1976. Destructive operations should eventually use directory-handle/openat-style primitives or platform equivalents for symlink-resistant mutation.

## Verification performed

### Commands and outcomes

- go test ./internal/bigfile/... — passed.
- go test -race ./internal/agent ./internal/workspace ./internal/bigfile/... — passed.
- go vet over the reviewed core packages and internal/bigfile/... — passed.
- Targeted tests for internal/paths, netsafe, secret, settings, artifacts, watcher, terminal, datatools, sqlguard, db, gitsvc, workspace, agent, and llm — passed or reported no test files.
- internal/jobs could not be executed in this environment: Windows Defender quarantined/blocked the generated test executable as containing a virus or potentially unwanted software. This is a test-evidence gap, not evidence that the package is malicious.
- go test ./... exceeded the 124-second review timeout without producing a final result, so the repository-wide test run is not confirmed green.

### Important blind spots

- No live PostgreSQL/MySQL servers were used, so transport and SQL-guard findings are based on code paths and documented engine semantics.
- No Wails UI automation or packaged release binary was run.
- Crash consistency was inspected statically; power-loss/fault injection was not available.
- Race tests cover packages’ existing tests, but existing tests do not exercise watcher shutdown, concurrent Big File RPC lifecycle, workspace switching during long operations, or agent cancel-then-immediate-restart.
- Several infrastructure packages (watcher, gitsvc, llm, netsafe, secret, terminal) have no dedicated *_test.go files despite holding security/lifecycle logic.

## Positive observations

- internal/paths and workspace mutation code consistently attempt lexical containment, real-path rechecks, and root-operation rejection.
- LLM Ask mode already has a good plaintext-key refusal, redirect policy, first-byte/idle timeout split, and bounded non-2xx error-body read. Agent mode should reuse that implementation.
- Database queries have a row cap, timeout, SQL normalization, and engine-specific read-only execution; SQLite pins PRAGMA query_only to the same connection and network engines fail closed if a read-only transaction cannot start.
- Git sets a noninteractive environment and rejects network Git operations, reducing prompt hangs and unintended network access.
- Workspace editor saves use a same-directory temp file, fsync the temp, and revision hashes. Extending that helper to preserve metadata and to all producers would resolve several findings.
- The terminal output bridge uses backpressure and batching, and terminal/watcher services have explicit shutdown hooks.
- Newer Big File export/replace/CSV/SQL plugin paths often reject same-file destinations, use exclusive creation, bound document reads, expose context-aware operations, and have extensive tests. The main reliability problem is inconsistent use of those stronger helpers by legacy and facade paths.
- The Big File document read API has a 64 MiB default range guard and reader lifecycle accounting; the session registry needs to preserve those guarantees across swaps/closes.

## Recommended remediation order

1. Stop credential disclosure: typed secret capabilities, endpoint binding, and Agent HTTPS parity (BE-001, BE-005).
2. Prevent irreversible file loss: reject same-file output everywhere and make all producer commits atomic (BE-003, BE-014, BE-030).
3. Disable or harden automatic .qrp replay before opening untrusted files (BE-002).
4. Narrow bound Wails facades so internal raw/producers are not renderer-callable (BE-004).
5. Fix SQL safety: quoted identifier bypass, legacy SQL escaping, syntax-aware dump transforms, and non-contiguous extraction (BE-007, BE-016 through BE-020).
6. Enforce acquisition-time byte budgets and cancellation for provider, command, query, table, Git, and Big File paths (BE-008, BE-009, BE-011, BE-012, BE-021, BE-031, BE-032, BE-038, BE-039).
7. Add lifecycle generations/leases and await shutdown across agent runs, workspace switches, watcher loops, and Big File sessions (BE-010, BE-027 through BE-029, BE-037).
8. Make persistent state transactional and recoverable with fault-injection tests (BE-023 through BE-026, BE-033, BE-041).

## Minimum regression suite to add

- Secret capability matrix: every ref type crossed with every consumer and endpoint-change path; all cross-purpose combinations must fail.
- Same-file output matrix: identical path, canonical alias, case alias, symlink, hardlink, pre-existing destination, disk-full, cancel, and source-shrink for every file producer.
- Malicious .qrp corpus: stale identity, huge count/length, negative/overflow offset, overlap, truncated payload, committed flag variants, and partial replay.
- SQL guard corpus per engine: quoted/schema-qualified dangerous calls, comments between identifier and call, nested expressions, Unicode escapes, CTEs, and known side-effect functions.
- Dump fixtures: all DDL then all data, interleaved tables, repeated COPY/INSERT blocks, duplicate table names across schemas, huge single-line INSERT, incomplete COPY, and PostgreSQL escape variants.
- Concurrency/race scenarios: Cancel then immediate Start; workspace Open during table/artifact/Git work; concurrent Big File Get/Refresh/Close/Edit; watcher shutdown during event delivery.
- Fault injection at every write/fsync/rename/delete phase for settings, secrets, DB profiles, artifacts, transform outputs, editor saves, replace manifests, and recovery.
- Byte-budget tests: oversized provider response, model list, command output, Git diff/stderr, DB cell, table row/cell, search hit count, edit window, and rollback source.
