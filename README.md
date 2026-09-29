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
- Big-file engine with bounded read/bridge windows for large files: a windowed
  viewer with true line numbers, in-file search, go-to
  line/offset/percent, hex view, follow-tail, and encoding detection; per-line
  syntax highlighting (~45 languages + SQL) and rainbow-CSV grids; and
  copy-only staged editing that leaves the source untouched. Practical limits
  depend on the operation, file structure, filesystem, memory, and free disk
  space. See [Large-file engine](docs/big-files.md).
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
  and constant-column export — plus a bounded, streaming Data Tools suite
  (Tools menu) for large CSV/SQL files. The large-file suite provides CSV
  filter/dedupe/sample/redact/profile, JSONL and CSV-to-SQL export, SQL
  lint/extract/split/reshape/sample, serialization-aware plain SQL
  find/replace, and regex harvesting. Large-file SQLite/XLSX export, structural
  cleanup presets, and regex SQL replacement remain disabled.
- Database connections for SQLite/Postgres/MySQL with encrypted credentials,
  schema browsing, guarded read-only queries, and virtualized result grids.
- Artifact registry for produced files and lineage/freshness tracking.

## Stack

- Go 1.27.1
- Wails v3.0.0-beta.23
- React 19 + TypeScript 6 + Vite 8
- Monaco editor
- xterm.js
- Zustand
- lucide-react

## Prerequisites

- Go 1.27.1
- Node 24.21.x LTS with npm 11.x
- macOS 13 or later when building or running the macOS package
- Linux packages use GTK 4 and WebKitGTK 6.0 (Ubuntu 24.04+ or Debian 13+)
- Wails v3 and Task CLIs:

```powershell
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23
go install github.com/go-task/task/v3/cmd/task@v3.53.1
```

## Development

Run these commands from the repository root. Root Task commands delegate to the
application in `src/`; direct Go and Wails commands must run inside `src/`.

Install frontend dependencies:

```powershell
npm --prefix src/frontend ci
```

Run the app with hot reload:

```powershell
task dev
```

Run frontend checks:

```powershell
npm --prefix src/frontend run typecheck
npm --prefix src/frontend run test
```

Run focused backend tests:

```powershell
Push-Location src
go test ./internal/settings ./internal/agent ./internal/datatools ./internal/db
Pop-Location
```

Run all backend tests:

```powershell
Push-Location src
go test . ./internal/...
Pop-Location
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

### Target support status

- Windows, macOS, and Linux desktop source builds are the maintained CI targets.
  Public packaging is still frozen until the release gates described below are
  complete.
- iOS simulator and Android builds are experimental and are not CI-verified.
  Android production packaging fails closed until release signing and artifact
  verification exist; the explicitly named debug task is for local testing only.
  See [Android build status](src/build/android/README.md) for the current toolchain
  and the remaining Wails mobile-host/security blocker.
- Windows MSIX packaging is disabled until a reviewed Wails v3 configuration,
  signing identity, and clean-machine install/upgrade checks are committed. NSIS
  is the only selectable Windows package format in the current task source.
- Linux ARM64 Docker builds use a target-platform image and reject runtime,
  compiler-target, or output-ELF mismatches. A real/emulated ARM64 launch test is
  still required before claiming platform support.
- Server mode remains disabled. A task's presence is not a support claim.

If Go service types or method signatures change, regenerate TypeScript bindings:

```powershell
Push-Location src
wails3 generate bindings -f '-tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"' -clean=true -ts
Pop-Location
```

The generated bindings live in `src/frontend/bindings/` and are committed so the
frontend remains type-safe without requiring every contributor to regenerate
them before editing UI code.

## CI and Release

GitHub Actions runs CI on `master` and pull requests across Windows, macOS, and
Linux. Each runner installs the pinned Go, Node, Task, and Wails versions, then
runs backend tests, frontend checks, and a production build.

`.github/workflows/release.yml` describes the intended tag packaging flow, but
publication is deliberately blocked by a failing release gate. Do not expect a
tag to publish until exact-commit validation, version/platform identity checks,
signing, SBOM, and provenance gates are implemented and the freeze is reviewed.
The eventual tag shape is:

```powershell
git tag v0.1.0
git push origin v0.1.0
```

Once that gate is deliberately enabled, the workflow is intended to build
native artifacts on each OS, upload them, and let GoReleaser publish verified
prebuilt assets. The current workflow must not be represented as release-ready.

## Project Layout

```text
Taskfile.yml                    Root build/dev/run and platform-task facade
.github/                        CI, release workflows, and contribution templates
docs/                           Architecture, runtime, and project policies
src/
  Taskfile.yml                  Application task definitions
  go.mod, go.sum                Go module and dependency lockfile
  main.go                       Wails bootstrap and service wiring
  secret_service.go             Narrow frontend-facing secret API
  shell_service.go              Native shell helpers and folder picker
  internal/
    agent/, llm/               Agent approvals/tool loop and provider integration
    bigfile/, datatools/        Bounded large-file engine and CSV/SQL tools
    db/, gitsvc/, terminal/     Database, Git, and PTY/ConPTY services
    workspace/, watcher/       Workspace containment and filesystem events
    secret/, settings/         Credentials and non-secret preferences
    artifacts/, jobs/          Artifact lineage and background-job ledger
    netsafe/, sqlguard/        Network and read-only SQL safety boundaries
  frontend/
    src/components/             Workbench views and panels
    src/state/store.ts          App state and async actions
    src/lib/                    Bindings adapters, Monaco setup, utilities
    bindings/                   Committed generated Wails bindings
  build/                        Native build configuration and packaging assets
bin/                            Ignored build artifacts (still at repository root)
```

### Layout migration

The Go module, frontend, native assets, and application Taskfile moved under
`src/`. Update IDE module roots and local scripts that previously ran Go/Wails
from the repository root or npm from `frontend/`. Go import paths remain
`novera/...`; this is a repository-layout change, not a module rename. Root
`task build`, `task dev`, `task run`, and platform tasks remain the entry points,
and build artifacts remain under root `bin/`.

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

Large-file engine state is stored separately under the same OS config root at
`Novera/bigfile` and can be redirected with `NOVERA_BIGFILE_HOME`. On first use,
when canonical Novera state is absent, parseable legacy settings are sanitized
and copied forward, matching line-index caches from `QUARRY_HOME` or the former
`~/.quarry` directory are republished, and at most a bounded recent portion of
the legacy log is imported. Existing canonical state wins, legacy inputs are
never modified, malformed settings/caches are not imported, non-regular inputs
are rejected, and malformed log lines are skipped when diagnostics are read.
Setting `NOVERA_BIGFILE_HOME` disables implicit `~/.quarry` discovery unless
`QUARRY_HOME` is also set explicitly. Existing `.quarry.*` recovery sidecars
remain readable so an upgrade does not strand recovery data.

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
- `src/frontend/dist/`
- `src/frontend/node_modules/`
- `.task/`
- `.gotmp/`
- `.idea/`
- `.claude/`
- `.tmp-test-files/`

Do not commit local datasets, generated build outputs, IDE metadata, or
temporary LLM/tool scratch files.

## Project policies

- [Security reporting and supported versions](SECURITY.md)
- [Contribution requirements](CONTRIBUTING.md)
- [Curated changelog](CHANGELOG.md)
- [Community conduct](CODE_OF_CONDUCT.md)
- [Release checklist](docs/release-checklist.md)
- [Wails runtime containment and upgrade policy](docs/wails-runtime-policy.md)
- [Large-file performance evidence](docs/performance.md)
- [Project analysis and dependency migration, 2026-09-20](docs/project-analysis-2026-09-20.md)
- [Diagnostics and support-data policy](docs/diagnostics-policy.md)
