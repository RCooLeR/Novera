// A tiny module-level handle to the active Monaco editor so commands outside the
// editor component (command palette, menu) can act on it — e.g. Format Document.
import type { OnMount } from "@monaco-editor/react";

type Ed = Parameters<OnMount>[0];

let current: Ed | null = null;

export function setActiveEditor(ed: Ed | null) {
  current = ed;
}

export async function formatActiveDocument(): Promise<void> {
  const ed = current;
  if (!ed) return;
  try {
    const action = ed.getAction("editor.action.formatDocument");
    if (action) await action.run();
  } catch {
    // The editor may have been disposed between capture and run (e.g. the tab
    // closed) — the optional-chaining only guarded a null handle, not a disposed
    // one. Swallow rather than surfacing an unhandled rejection.
  }
}
