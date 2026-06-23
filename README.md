# Novera

Novera is a local-first AI workbench for working with code, data files, SQL
dumps, databases, terminals, and local or OpenAI-compatible LLMs from one
desktop app.

The app is built with a native Go backend and a React/TypeScript frontend in
Wails v3. The backend owns filesystem, Git, terminal, database, LLM, and
mutation safety. The frontend provides the editor/workbench UI with Monaco,
xterm.js, virtualized grids, and generated Wails bindings.

## Current Capabilities

- Workspace explorer with lazy tree loading, new file/folder, rename, delete,
  stale-save protection, encoding conversion, and large/binary file handling.
- Monaco editor tabs, diff viewer, breadcrumbs, status bar, in-app
  File/Edit/View/Tools/Help menus, command palette, quick-open, and global
  workspace search.
- Big-file engine (memory-bounded streaming) that opens and edits files of any
  size: a windowed viewer with true line numbers, in-file search, go-to
  line/offset/percent, hex view, follow-tail, and encoding detection; per-line
  syntax highlighting (~45 languages + SQL) and rainbow-CSV grids; and editing
  with crash-safe in-place patch or save-as-copy. See
  [Large-file engine](docs/big-files.md).
- Animated boot splash shown over startup until the backend finishes
  initializing.
- Source Control panel with status, stage/unstage, commit, and changed-file
  diffs.
- Integrated terminal backed by a native PTY/ConPTY.
- Problems panel for TODO/FIXME/HACK/XXX and merge-conflict markers.
- AI Ask mode for chat against Ollama, OpenAI, or a custom OpenAI-compatible
  endpoint, with active-file context.
- AI Agent mode with contextual tool selection, approval-gated mutations,
  approval-gated HTTP requests, visible plan updates, audit log, Jobs log
  integration, and configurable runtime limits for slow local models.
- Data tools for CSV/TSV schema inference, CSV-to-SQL generation, SQL dump
  analysis, dump cleaning, table extraction, dump splitting, CSV projection,
  and constant-column export — plus a streaming Data Tools suite (Tools menu)
  that works on files of any size: CSV filter/dedupe/sample/redact/profile and
  JSONL/SQLite/XLSX export, and SQL lint/extract/split/reshape/preset cleanups
  and regex harvesting.
- Database connections for SQLite/Postgres/MySQL with encrypted credentials,
  schema browsing, guarded read-only queries, and virtualized result grids.
- Artifact registry for produced files and lineage/freshness tracking.

## Stack

- Go 1.25+
- Wails v3 alpha
- React 18 + TypeScript + Vite
- Monaco editor
- xterm.js
- Zustand
- lucide-react

## Prerequisites

- Go 1.25+
- Node 18+
- Wails v3 CLI:

```powershell
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
```

## Development

Install frontend dependencies:

```powershell
cd frontend
npm install
```

Run the app with hot reload:

```powershell
cd ..
wails3 dev -config ./build/config.yml -port 9245
```

Run frontend checks:

```powershell
cd frontend
npm run typecheck
npm run test
```

Run focused backend tests:

```powershell
go test ./internal/settings ./internal/agent ./internal/datatools ./internal/db
```

Run all backend tests:

```powershell
go test ./...
```

## Build

Production Windows build:

```powershell
task build
```

The executable is written to:

```text
bin/Novera.exe
```

If Go service types or method signatures change, regenerate TypeScript bindings:

```powershell
wails3 generate bindings -f '-tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"' -clean=true -ts
```

The generated bindings live in `frontend/bindings/` and are committed so the
frontend remains type-safe without requiring every contributor to regenerate
them before editing UI code.

## CI and Release

GitHub Actions runs CI on `master` and pull requests across Windows, macOS, and
Linux. Each runner installs the pinned Go, Node, Task, and Wails versions, then
runs backend tests, frontend checks, and a production build.

Tagged releases use `.github/workflows/release.yml`:

```powershell
git tag v0.1.0
git push origin v0.1.0
```

The release workflow builds native artifacts on each OS, uploads them to the
workflow, then runs GoReleaser to create the GitHub release from those prebuilt
assets. This keeps Wails platform packaging native while GoReleaser owns release
notes and GitHub artifact publication.

## Project Layout

```text
main.go                         Wails app bootstrap and service wiring
secret_service.go               Secret API exposed to the frontend
shell_service.go                Native shell helpers and folder picker
internal/
  agent/                        Agent mode, tool loop, approvals, audit, rollback
  artifacts/                    Artifact registry and freshness checks
  bigfile/                      Streaming engine for files of any size: windowed
                                read/edit, line index, piece-table edits,
                                crash-safe in-place patch, hex, CSV/SQL tools
  datatools/                    CSV and SQL dump inspection/transforms
  db/                           Database profiles, read-only query service
  gitsvc/                       Git status/diff/stage/commit service
  jobs/                         Background job ledger and logs
  llm/                          Ask-mode streaming and provider integration
  netsafe/                      Endpoint/redirect safety helpers for LLM/HTTP
  secret/                       OS-backed secret storage
  settings/                     Persisted non-secret preferences
  sqlguard/                     Single read-only SQL query guard
  terminal/                     PTY/ConPTY terminal service
  watcher/                      Filesystem watcher bridge
  workspace/                    Workspace path containment and file APIs
frontend/
  src/components/               Workbench views and panels
  src/state/store.ts            App state and async actions
  src/lib/                      Bindings re-exports, Monaco setup, utilities
  bindings/                     Generated Wails TypeScript bindings
build/                          Wails build config and platform packaging files
```

## Safety Model

Novera is local-first and intentionally conservative around user data:

- Workspace paths are resolved through containment checks before reads/writes.
- Saves are atomic and guarded by a revision hash to avoid stale clobbers.
- Secrets are stored in the OS secret store, not plaintext settings.
- Agent file mutations, shell commands, sensitive DB reads, and outbound HTTP
  requests require explicit approval.
- Agent tool results are clipped, logged, and audited; raw tool bodies are not
  shown in the assistant log.
- Denied agent actions stop the run cleanly instead of attempting fallback
  mutations.
- Database user queries pass `sqlguard` and run through read-only execution
  paths.

See [Agent Runtime](docs/agent-runtime.md) for Agent mode behavior and tuning.

## Runtime Settings

Settings are stored under the OS user config directory, for example
`%AppData%/Novera/settings.json` on Windows. Credential material is stored
separately by the secret service.

Important local-LLM controls:

- Provider defaults: new installs start on local Ollama at
  `http://localhost:11434/v1` with `gemma4:12b-it-q8_0`; OpenAI and custom
  OpenAI-compatible providers keep their model choice explicit.
- Request timeout: per LLM request/agent completion timeout.
- Tool output chars: maximum tool result text sent back to the model.
- Step batch: how many tool rounds run before asking whether to continue.
- Max steps: hard runaway limit for one agent run.
- History window: number of recent assistant/tool rounds sent to the model.
- Command timeout: maximum duration for one approved shell command.

## Repository Hygiene

Generated and local-only folders are ignored:

- `bin/`
- `frontend/dist/`
- `frontend/node_modules/`
- `.task/`
- `.gotmp/`
- `.idea/`
- `.claude/`
- `.tmp-test-files/`

Do not commit local datasets, generated build outputs, IDE metadata, or
temporary LLM/tool scratch files.
