import { useEffect, useRef, useState } from "react";
import { useStore } from "../state/store";
import { validateFileName } from "../lib/validate";

export default function FileOpModal() {
  const op = useStore((s) => s.pendingFileOp);
  const submit = useStore((s) => s.submitFileOp);
  const cancel = useStore((s) => s.cancelFileOp);
  const [value, setValue] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const error = value.trim() ? validateFileName(value) : "";
  const canSubmit = !!value.trim() && !error;

  useEffect(() => {
    if (op) {
      setValue(op.initial);
      const id = setTimeout(() => {
        const el = inputRef.current;
        if (el) {
          el.focus();
          const dot = op.initial.lastIndexOf(".");
          el.setSelectionRange(0, dot > 0 ? dot : op.initial.length);
        }
      }, 0);
      return () => clearTimeout(id);
    }
  }, [op]);

  // Window-level Escape (works regardless of which control has focus) and a Tab
  // focus trap so keyboard focus can't leave the dialog while it's open.
  useEffect(() => {
    if (!op) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        cancel();
        return;
      }
      if (e.key === "Tab") {
        const focusable = dialogRef.current?.querySelectorAll<HTMLElement>("input, button:not([disabled])");
        if (!focusable || focusable.length === 0) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [op, cancel]);

  if (!op) return null;

  return (
    <div className="modal-overlay" onMouseDown={cancel}>
      <div
        ref={dialogRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label={op.title}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="modal__title">{op.title}</div>
        <input
          ref={inputRef}
          className="modal__input"
          value={value}
          spellCheck={false}
          aria-invalid={!!error}
          placeholder={op.type === "newFolder" ? "folder name" : "file name"}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (canSubmit) void submit(value);
            }
          }}
        />
        {error && <div className="modal__error">{error}</div>}
        <div className="modal__actions">
          <button className="btn btn--primary" onClick={() => void submit(value)} disabled={!canSubmit}>
            {op.type === "rename" ? "Rename" : "Create"}
          </button>
          <button className="btn" onClick={cancel}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
