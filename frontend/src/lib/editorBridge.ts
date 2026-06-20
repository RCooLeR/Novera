// A tiny module-level handle to the active Monaco editor so commands outside the
// editor component (command palette, menu) can act on it — e.g. Format Document.
import type { OnMount } from "@monaco-editor/react";

type Ed = Parameters<OnMount>[0];

let current: Ed | null = null;

export function setActiveEditor(ed: Ed | null) {
  current = ed;
}

export async function runActiveEditorAction(actionId: string): Promise<boolean> {
  const ed = current;
  if (!ed) return false;
  try {
    ed.focus();
    const action = ed.getAction(actionId);
    if (!action) return false;
    await action.run();
    return true;
  } catch {
    // The editor may have been disposed between capture and run (e.g. the tab
    // closed) — the optional-chaining only guarded a null handle, not a disposed
    // one. Swallow rather than surfacing an unhandled rejection.
    return false;
  }
}

export async function formatActiveDocument(): Promise<void> {
  await runActiveEditorAction("editor.action.formatDocument");
}
