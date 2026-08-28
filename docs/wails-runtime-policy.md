# Wails runtime containment and upgrade policy

Novera currently pins Wails `v3.0.0-beta.15`. It is still a pre-stable
dependency, so updates are isolated security/compatibility migrations rather
than routine version bumps. The Go module, frontend runtime, CLI, generated
bindings, and build images must move together.

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
