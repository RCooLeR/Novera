# Novera (Wails v3)

Novera is a local-first AI workbench — an IDE-like desktop app. This is the
**Wails v3 + React/TypeScript** rebuild of the original Go/Fyne `Novera_v2`,
chosen so the hard UI surfaces (code editor, terminal, diff) can use mature web
components (Monaco, xterm.js, Monaco diff) instead of hand-built canvas widgets.

The Go backend stays native; the UI is web tech in a system WebView2 window.

## Stack

- **Backend:** Go 1.24+, [Wails v3](https://v3.wails.io) (alpha). Services in `internal/*`.
- **Frontend:** React 18 + TypeScript + Vite, [Monaco editor](https://microsoft.github.io/monaco-editor/), zustand, lucide icons.
- **Windows runtime:** WebView2 (preinstalled on Windows 11).

## Prerequisites

- Go 1.24+ and Node 18+
- `wails3` CLI (`go install github.com/wailsapp/wails/v3/cmd/wails3@latest`)

## Run (dev, hot reload)

```powershell
cd E:\Development\projects\apps\rcooler\Novera_Wails3
wails3 dev -config ./build/config.yml -port 9245
```

## Build (production Windows exe)

```powershell
wails3 task build        # -> bin\Novera.exe  (single self-contained binary)
```

After changing any Go service signature, regenerate the TypeScript bindings:

```powershell
wails3 generate bindings -ts
```

## Project layout

```
main.go                     App bootstrap: window + service registration
shell_service.go            Shell service: native folder picker, open-external
internal/
  paths/                    Workspace-rooted path containment (securejoin)
  workspace/                File service: list/read/write, atomic + stale-guarded
  settings/                 Persisted preferences (%AppData%/Novera/settings.json)
frontend/
  src/
    lib/        monaco.ts (workers+theme), services.ts (bindings), lang.ts
    state/      store.ts (zustand: workspace, tree, tabs, editor, ui)
    components/ TitleBar, ActivityBar, SideBar, FileTree, EditorTabs,
                EditorPane (Monaco), StatusBar, Welcome
  bindings/                 Auto-generated TS bindings (do not edit)
```

## Architecture

Services own all filesystem/OS access and never touch the UI; the React layer
calls them through generated, type-safe bindings (async Promises). This mirrors
the clean services/UI split from the previous generation — which is why the Go
logic is portable — while replacing the entire view layer with web components.

### Fixes carried over by design (from the `Novera_v2` review)

- **Path containment:** all UI paths resolve through `securejoin` — no symlink
  escape from the workspace root.
- **UTF-8-safe reads:** binary sniffing + size caps; no mid-rune truncation.
- **Atomic writes + optimistic concurrency:** writes go temp→rename, guarded by
  a content-hash revision so a stale save can't clobber on-disk changes.
- **Secrets never in plain config:** settings hold no credential material.

## Status / roadmap

Implemented: app shell, dark theme + branding, Open Folder, lazy file tree,
Monaco editor with tabs + save (stale-guarded), recents, settings persistence,
**Source Control** (status / stage / unstage / commit, with the changed-file
diff rendered in Monaco's diff editor; hardened non-interactive `git` with
correct `-z`/`core.quotepath` porcelain parsing), an **integrated terminal**
(real ConPTY/PTY shell via go-pty, xterm.js frontend, bottom panel, Ctrl+`),
an **AI assistant** (right-hand panel, streaming chat against any
OpenAI-compatible provider — Ollama/OpenAI/custom — with the active file sent as
context; API keys live in a dedicated DPAPI-encrypted secret store, never in
settings or chat history), and **Database** connections + read-only query
(SQLite/Postgres/MySQL; queries pass `sqlguard`'s single read-only-SELECT check
AND run inside a read-only transaction; credentials in the encrypted secret
store; results in a virtualised grid; click a table to query it),
**file operations** (right-click / header: new file·folder, rename, delete —
via inline modals since WebView2 blocks `window.prompt`; rename remaps open
tabs), and a **command palette + quick-open** (Ctrl+P fuzzy file open, Ctrl+Shift+P
commands; fed by a recursive `ListAllFiles` that skips heavy dirs).

**workspace search** (Go grep skipping heavy dirs/binaries; results grouped by
file; click a match to open + reveal the line in Monaco), **Problems** (static
TODO/FIXME/HACK/XXX + merge-conflict scan in the bottom panel, anchored to avoid
false positives), and assistant **Agent mode** (a tool-calling loop —
list/read/search/git-status run automatically, `write_file` is gated behind
per-call user approval; streamed to the panel with an Ask/Agent toggle and
inline Allow/Deny cards).

The core feature set is complete; ongoing work is review-driven hardening.
