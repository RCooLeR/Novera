# Wails runtime containment and upgrade policy

Novera currently pins Wails `v3.0.0-beta.34`. It is still a pre-stable
dependency, so updates are isolated security/compatibility migrations rather
than routine version bumps. The Go module, frontend runtime, CLI, generated
bindings, and build images must move together.

The application module is in [src/go.mod](../src/go.mod), frontend runtime
pins are in [src/frontend/package.json](../src/frontend/package.json), and
native assets are in [src/build/](../src/build/). Run direct Go/Wails commands
from `src/`; the repository-root Taskfile delegates there.

The beta.23 migration was prepared on 2026-09-20. The Go module, npm runtime,
and CLI pins agree; regenerated bindings preserve the existing 150-method
contract. Windows production compilation and local regression checks passed.
Native launch/dialog/event/close checks on Windows, macOS, and Linux and the
clean-host packaging matrix remain outstanding. This is a migration candidate,
not completion of the native acceptance checklist below. See the
[project analysis](project-analysis-2026-09-20.md) for evidence and limitations.

The 2026-10-20 update synchronizes the Go module, npm runtime, and CI/release CLI
at beta.31. Dependabot proposed beta.30 separately for Go and npm, leaving each
PR's version-alignment check failing. Beta.31 additionally fixes the published
macOS child-frame control-channel, Windows notification interpolation, CLI
template argument, and server-mode request-validation advisories.
`TestWailsVersionAlignment` checks these pins and the npm lockfile together so
independently proposed dependency updates cannot silently mix versions.
Native interaction and clean-host packaging checks remain release blockers;
automated tests and compilation alone do not complete that acceptance matrix.

The 2026-10-26 update synchronizes Go, npm, and the CI/release CLI at beta.34.
The [beta.32 release](https://github.com/wailsapp/wails/releases/tag/v3.0.0-beta.32),
[beta.33 release](https://github.com/wailsapp/wails/releases/tag/v3.0.0-beta.33),
and [beta.34 release](https://github.com/wailsapp/wails/releases/tag/v3.0.0-beta.34)
and their [combined source diff](https://github.com/wailsapp/wails/compare/v3.0.0-beta.31...v3.0.0-beta.34)
were reviewed for Novera's integration seams:

- Windows fixes preserve inherited WebView2 COM vtable slots, restore focus
  after activation and Tab handoff, and log transient runtime COM failures
  instead of terminating the process. These affect the main workbench webview;
  keyboard focus and native lifecycle behavior still need interactive checks.
- Windows custom question-dialog labels and callbacks now use TaskDialog with
  a MessageBox fallback. Novera uses native open/save dialogs and its own
  renderer close confirmation, not the question-dialog API.
- The generator can export typed names for events registered with
  `application.RegisterEvent`. Novera emits and validates string-named events
  without those registrations, so this does not require a bridge expansion.
- The development server's readiness check now uses a TCP probe, fixing Vite
  startup delays. The macOS menu-header/work-area and updater/systray changes
  do not introduce new Novera calls to those APIs.

This remains a development migration under the existing release freeze.
Automated build/contract checks do not establish native Windows/macOS/Linux
interaction, clean-host packaging, or performance/memory acceptance. Those
checks remain outstanding until their results are recorded; no release gate
or bridge allowlist is relaxed by this update.

## Containment boundary

Business logic must not gain direct Wails imports. The repository test
`TestWailsImportBoundary` records the existing integration seams and fails if a
new one appears. New runtime behavior should be expressed through a small
Novera-owned interface for events, dialogs, browser opening, lifecycle, or
service registration, then implemented at an existing integration seam.

`TestGeneratedBridgeMethodAllowlist` records every generated renderer-callable
method. A binding change is an authority change even if the TypeScript diff was
generated. Review backend validation, argument bounds, lifecycle ownership,
workspace/resource identity, cancellation, and secret handling before updating
the allowlist.

This boundary contains growth; it does not claim the remaining direct imports
or concrete service registration have already been migrated to final facades.

## Stable-migration milestone

The first stable Wails v3 release that supports all maintained desktop targets
opens a dedicated migration milestone. Public release remains blocked if the
current beta prevents a required security or platform update. Exit criteria:

1. Read upstream release/migration notes and enumerate API, binding, event,
   WebView, packaging, and platform changes.
2. Upgrade Go runtime, frontend runtime, and CLI pins together in one pull
   request; never mix application features into it.
3. Regenerate bindings from a clean export and review the complete bridge
   allowlist diff.
4. Pass Go/frontend tests, production CSP/entry assertions, and native
   Windows/macOS/Linux builds.
5. Exercise a minimal native fixture on every maintained OS: launch, single
   instance, service call success/error, event delivery and malformed-event
   rejection, open/save dialog cancel/success, external URL policy, window
   close/quit, and About build identity.
6. Package, install/extract, launch, and uninstall the candidate on clean hosts;
   confirm no server/listening transport was introduced.
7. Record performance and memory comparisons and update the changelog/support
   matrix before merging.

Beta-to-beta security updates use the same checklist. If an update cannot
pass, document the exact upstream blocker and keep release frozen; do not paper
over it by weakening tests or widening the bridge.
