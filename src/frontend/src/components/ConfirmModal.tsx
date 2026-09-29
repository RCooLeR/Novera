import { useRef, type ReactNode } from "react";
import { useDialogFocus } from "../lib/useDialogFocus";

// A small accessible confirmation dialog: role=dialog + aria-modal, autofocuses
// the primary action, closes on Escape, traps Tab within itself, and cancels on
// backdrop click. Shared by destructive confirmations across the app.
export default function ConfirmModal({
  title,
  body,
  confirmLabel = "OK",
  cancelLabel = "Cancel",
  danger = false,
  onConfirm,
  onCancel,
}: {
  title: string;
  body?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const confirmRef = useRef<HTMLButtonElement | null>(null);
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useDialogFocus(true, onCancel, danger ? cancelRef : confirmRef);

  return (
    <div className="modal-overlay" onMouseDown={onCancel}>
      <div
        ref={dialogRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="modal__title">{title}</div>
        {body != null && <div className="modal__text">{body}</div>}
        <div className="modal__actions">
          <button ref={confirmRef} className={`btn ${danger ? "btn--danger" : "btn--primary"}`} onClick={onConfirm}>
            {confirmLabel}
          </button>
          <button ref={cancelRef} className="btn" onClick={onCancel}>
            {cancelLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
