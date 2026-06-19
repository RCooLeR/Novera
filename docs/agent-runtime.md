# Agent Runtime

Agent mode is Novera's tool-calling workflow for workspace tasks. It uses an
OpenAI-compatible chat/completions endpoint and a curated set of backend tools.

## How Tool Selection Works

The frontend sends the current user request plus a small recent chat context to
the backend. The context exists only to resolve vague follow-ups such as
`do it`, `that`, or `write this`.

The backend then selects a smaller active tool set for the run:

- Inspect tools are always available.
- Write tools are added only when the request or recent context asks to create,
  edit, fix, patch, move, delete, or persist files.
- Git, data, database, artifact, rollback, and shell-command tools are added
  only when relevant keywords appear.
- A model calling a valid but inactive tool is rejected by dispatch.
- A model inventing pseudo-tools such as `thought`, `analysis`, or `channel` is
  retried once, then the run fails cleanly.

This keeps local models focused and reduces accidental tool misuse.

## Approval and Denial

Read-only workspace inspection runs automatically. The following actions are
approval-gated:

- File mutations
- Rollbacks
- Shell commands
- Sensitive database reads

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

Agent limits are configurable in Settings > Agent runtime:

- Tool output chars
- Step batch
- Max steps
- History window
- Command timeout

Each run writes the active tools and resolved limits into the Jobs log so slow
or stuck local-model behavior can be diagnosed later.

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
