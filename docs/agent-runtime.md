# Agent Runtime

Agent mode is Novera's tool-calling workflow for workspace tasks. It uses an
OpenAI-compatible chat/completions endpoint and a curated set of backend tools.

## How Tool Selection Works

The frontend sends the current user request plus a small recent chat context to
the backend. The context exists only to resolve vague follow-ups such as
`do it`, `that`, or `write this`.

The backend starts with a smaller active tool set, then lets it grow as the task
needs it:

- Inspect tools are always available.
- Write tools are added when the request or recent context asks to create, edit,
  fix, patch, move, delete, or persist files.
- Git, data, database, artifact, rollback, HTTP-request, and shell-command
  tools are added when relevant keywords appear.
- The active set only ever **grows** within a run. After each step it is
  re-derived from the whole conversation so far, and if the model calls a valid
  tool that wasn't pre-enabled, that tool is **activated on demand** (and run,
  still subject to approval) rather than rejected. A task that evolves to need a
  capability is no longer dead-ended; the system prompt also tells the model the
  other capability groups exist and turn on when needed.
- A model inventing pseudo-tools such as `thought`, `analysis`, or `channel` is
  retried once, then the run fails cleanly.

This keeps local models focused on a small initial set while letting capability
expand on demand — approval gating, not tool hiding, is the safety boundary.

## Approval and Denial

Read-only workspace inspection runs automatically. The following actions are
approval-gated:

- File mutations
- Rollbacks
- Shell commands
- Sensitive database reads
- Outbound HTTP requests

The HTTP request tool does not send cookies or stored credentials
automatically. It accepts only explicit headers, rejects URL-embedded
credentials and unsafe schemes, blocks cross-host redirects, caps request and
response sizes, and returns text bodies only when they look textual/UTF-8.

If the user denies an approval, the agent stops the run and logs a clear message
instead of trying a fallback write tool. This prevents loops such as
`write_file denied, trying append_file`.

## Provider Transport and Run Lifecycle

When a stored provider key is present, Agent mode refuses to send it over
plaintext HTTP to a remote host. HTTPS is required except for deliberate local
providers on `localhost`, `127.0.0.1`, or `::1`. The policy is checked both
before a run is admitted and immediately before every completion request.
Redirects cannot move a request to another host or downgrade HTTPS to HTTP.
Every new provider/tool connection resolves through a dial-time destination
policy: public hosts must resolve only to public unicast addresses, the exact
local-provider names may resolve only to loopback, and metadata, private,
link-local, multicast, documentation, benchmark, and mixed public/private DNS
answers fail closed. The vetted numeric address is dialed directly and remains
pinned for the lifetime of that pooled connection. Ambient HTTP proxy settings
are intentionally ignored because a proxy would resolve the target outside
this policy; explicit proxy support requires an equivalent target-verification
design before it can be enabled.

Canceling a run signals its context but does not immediately release the
single-active-run lease. A replacement run is rejected until the canceled
provider or tool invocation has returned and terminal session/job bookkeeping
has finished. Provider tool-call IDs are replaced with run-scoped IDs before
they become approval keys, so a delayed approval from an earlier run cannot
authorize a later action. Context-aware command and HTTP tools stop promptly;
a legacy synchronous tool that cannot observe context may still finish its
current call, and the lease remains held while it does so. Workspace reset
waits up to ten seconds for that lease; a timeout blocks the workspace switch
or close instead of letting old work cross into a new root.

Workspace changes also hold an explicit Agent admission barrier from reset
through the final Open/Close commit. Failed changes abort that barrier and
restore the pre-transition conversation; successful changes commit an empty
conversation. A canceled or crashed run similarly restores its immutable
pre-run transcript, so a partial or unpaired tool-call turn is never inherited
by the next request.

Ask-mode requests use the same fail-closed lifecycle principle. At most four
completion streams and two model-list requests can be active at once; a
canceled request keeps its slot until its HTTP/setup work has actually
returned. Application shutdown permanently closes admission, cancels every
pending setup or HTTP request, and waits up to ten seconds for cleanup. The
bridge payload is checked before JSON marshaling: at most 256 messages, 1 MiB
per message, 256 KiB of system text, roughly 1 MiB of attached workspace
context, and 4 MiB of aggregate normalized text are accepted.

Every Agent event carries a per-run monotonic sequence number. The renderer
buffers bounded early or out-of-order events until `Agent.Start` confirms the
run ID, then applies only a contiguous sequence. Ask-mode stream events use the
same per-request ordering contract. Canceling while Start or Send is still
awaiting its ID invalidates the UI attempt; if the backend ID arrives later, it
is immediately canceled and cannot resurrect the old stream.

## Visible Output Filtering

Some local models emit internal text that looks like tool reasoning, for
example:

```text
The user denied the write_file request...
Wait, I see what happened...
<channel|>
```

Novera filters these tails before showing assistant output and also filters the
copy-to-clipboard path.

## Runtime Limits

New installs default to local Ollama at `http://localhost:11434/v1` and prefer
`gemma4:12b-it-q8_0`, with `gemma4:12b` kept as a secondary local option in
the UI. OpenAI and custom OpenAI-compatible providers do not inherit those local
model names; their selected model should come from the provider or be entered
explicitly.

Agent limits are configurable in Settings > Agent runtime:

- Tool output chars
- Step batch
- Max steps
- History window
- Command timeout

Each run writes the active tools and resolved limits into the Jobs log so slow
or stuck local-model behavior can be diagnosed later.

Provider-response diagnostics are disabled by default. For a short, deliberate
diagnostic session they can be enabled by setting
`NOVERA_AGENT_DEBUG_RESPONSES=1` before starting Novera. The opt-in log writes
JSONL to:

```text
<OS user config directory>/Novera/agent-debug.jsonl
```

On Windows the user config directory is normally `%AppData%`; macOS and Linux
use the directory returned by the platform's standard user-config lookup.

The debug log records response metadata plus a redacted structural preview. It
does not persist request bodies, authorization headers, model content, tool
arguments, URL query strings, or non-JSON response text. Preview entries are
capped at 64 KiB. The log rotates at 4 MiB and retains one 4 MiB backup, so its
on-disk footprint remains bounded. Disable the environment variable again and
restart Novera after diagnosis. Provider-debug files are session-scoped and the
next startup removes both `agent-debug.jsonl` and `agent-debug.jsonl.1`, which
also clears raw-response logs left by older versions.

The always-on Agent action audit is separate from provider diagnostics. It
stores structured operation metadata and outcome sizes, not command text, SQL,
search queries, request bodies/headers, tool output, or error text. On startup,
older free-form JSONL entries are atomically rewritten to omit their payloads;
malformed and oversized entries are dropped. The journal compacts to the newest
4 MiB at startup and whenever its live size would exceed 8 MiB. Its directory
and file are restricted to user-only permissions on platforms that expose
POSIX-style modes.

Defaults are intentionally generous for local models:

```text
tool output chars: 6000
step batch:        50
max steps:         1000
history window:    8
command timeout:   60 seconds
```

Shell output is capped while stdout/stderr are being drained rather than after
an unbounded allocation. Every command receives an owned process tree (a Job
Object on Windows and a process group on Unix); cancellation, timeout, detached
background output, and normal cleanup terminate remaining descendants. Launch,
timeout, cancellation, and non-zero exit outcomes are recorded as errors while
retaining the bounded diagnostic prefix.

Settings are clamped when saved and when loaded, so hand-edited config files
cannot create unsafe or unusable values.
