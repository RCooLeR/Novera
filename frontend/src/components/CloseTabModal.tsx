import { useEffect, useRef } from "react";
import { useStore } from "../state/store";

// Shown when closing an editor tab with unsaved edits, so changes aren't lost
// silently. Offers Save & Close / Don't Save / Cancel.
export default function CloseTabModal() {
  const path = useStore((s) => s.pendingTabClose);
  const tab = useStore((s) => s.tabs.find((t) => t.path === s.pendingTabClose) ?? null);
  const confirmCloseTab = useStore((s) => s.confirmCloseTab);
  const cancelCloseTab = useStore((s) => s.cancelCloseTab);
  const saveRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!path) return;
    saveRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        cancelCloseTab();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [path, cancelCloseTab]);

  if (!path) return null;

  return (
    <div className="modal-overlay" onMouseDown={cancelCloseTab}>
      <div className="modal" role="dialog" aria-modal="true" aria-label="Unsaved changes" onMouseDown={(e) => e.stopPropagation()}>
        <div className="modal__title">Save changes to {tab?.name ?? "this file"}?</div>
        <div className="modal__text">Your changes will be lost if you don't save them.</div>
        <div className="modal__actions">
          <button ref={saveRef} className="btn btn--primary" onClick={() => void confirmCloseTab(true)}>
            Save & Close
          </button>
          <button className="btn btn--danger" onClick={() => void confirmCloseTab(false)}>
            Don't Save
          </button>
          <button className="btn" onClick={cancelCloseTab}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
