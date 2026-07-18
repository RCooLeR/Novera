import { useRef } from "react";
import { useStore } from "../state/store";
import { useDialogFocus } from "../lib/useDialogFocus";

// Shown when closing an editor tab with unsaved edits. Large-file staging is
// deliberately saved from its editor, where the user can choose in-place or a
// copy; this dialog only permits an explicit discard.
export default function CloseTabModal() {
  const path = useStore((state) => state.pendingTabClose);
  const saving = useStore((state) => state.pendingTabCloseSaving);
  const tab = useStore((state) => state.tabs.find((item) => item.path === state.pendingTabClose) ?? null);
  const confirmCloseTab = useStore((state) => state.confirmCloseTab);
  const cancelCloseTab = useStore((state) => state.cancelCloseTab);
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useDialogFocus(!!path, cancelCloseTab, cancelRef);
  const largeFileDirty = !!tab?.largeFileDirty;

  if (!path) return null;

  return (
    <div className="modal-overlay" onMouseDown={cancelCloseTab}>
      <div
        ref={dialogRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label="Unsaved changes"
        aria-busy={saving}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="modal__title">
          {largeFileDirty
            ? `Discard staged edits to ${tab?.name ?? "this file"}?`
            : `Save changes to ${tab?.name ?? "this file"}?`}
        </div>
        <div className="modal__text">
          {largeFileDirty
            ? "Large-file edits must be saved from the editor. Discard & Close is explicit and cannot be undone."
            : "Your changes will be lost if you don't save them."}
        </div>
        <div className="modal__actions">
          {!largeFileDirty && (
            <button className="btn btn--primary" disabled={saving} onClick={() => void confirmCloseTab(true)}>
              {saving ? "Saving…" : "Save & Close"}
            </button>
          )}
          <button className="btn btn--danger" disabled={saving} onClick={() => void confirmCloseTab(false)}>
            {largeFileDirty ? (saving ? "Discarding…" : "Discard & Close") : "Don't Save"}
          </button>
          <button ref={cancelRef} className="btn" disabled={saving} onClick={cancelCloseTab}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
