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

Temporary raw provider-response logging is also enabled for Agent completions
while the local-model final-answer issue is being debugged. It writes JSONL to:

```text
%AppData%/Novera/agent-debug.jsonl
```

The debug log records response metadata and the raw provider response body. It
does not persist request bodies or authorization headers.

Defaults are intentionally generous for local models:

```text
tool output chars: 6000
step batch:        50
max steps:         1000
history window:    8
command timeout:   60 seconds
```

Settings are clamped when saved and when loaded, so hand-edited config files
cannot create unsafe or unusable values.
