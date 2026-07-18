import { useRef } from "react";
import { Check, RefreshCw, ShieldCheck, X } from "lucide-react";
import { useStore } from "../state/store";
import { useDialogFocus } from "../lib/useDialogFocus";

// Decision badge color: denied = red, approved = amber (a mutation you allowed),
// auto = muted (a read tool that ran without a gate).
function decisionClass(decision: string): string {
  if (decision === "denied") return "audit__badge audit__badge--denied";
  if (decision === "approved") return "audit__badge audit__badge--approved";
  return "audit__badge audit__badge--auto";
}

function fmtTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

// Read-only viewer over the persisted agent audit log (Agent.AuditLog) — the
// durable record of every agent tool action and which mutations were
// approved/denied.
export default function AuditLogModal() {
  const open = useStore((s) => s.auditOpen);
  const entries = useStore((s) => s.auditEntries);
  const loading = useStore((s) => s.auditLoading);
  const close = useStore((s) => s.closeAuditLog);
  const reload = useStore((s) => s.openAuditLog);
  const closeRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useDialogFocus(open, close, closeRef);

  if (!open) return null;

  return (
    <div className="modal-overlay" onMouseDown={close}>
      <div
        ref={dialogRef}
        className="modal toolsmodal"
        role="dialog"
        aria-modal="true"
        aria-label="Agent audit log"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="toolsmodal__head">
          <span className="modal__title">
            <ShieldCheck size={15} style={{ verticalAlign: "-2px", marginRight: 6 }} />
            Agent audit log
          </span>
          <span style={{ flex: 1 }} />
          <button className="icon-btn" title="Refresh" aria-label="Refresh" onClick={() => void reload()}>
            <RefreshCw size={14} aria-hidden focusable={false} />
          </button>
          <button ref={closeRef} className="icon-btn" title="Close" aria-label="Close" onClick={close}>
            <X size={15} aria-hidden focusable={false} />
          </button>
        </div>
        <div className="toolsmodal__body">
          <div className="toolsmodal__meta">
            Every tool the agent ran, newest first. Approved/denied mark mutations you gated; auto are read-only tools.
          </div>
          {loading ? (
            <div className="dbq__empty">Loading…</div>
          ) : entries.length === 0 ? (
            <div className="dbq__empty">No agent actions recorded yet.</div>
          ) : (
            <table className="toolsmodal__table audit__table">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Tool</th>
                  <th>Target</th>
                  <th>Decision</th>
                  <th>Result</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e, i) => (
                  <tr key={i}>
                    <td className="audit__time">{fmtTime(e.time)}</td>
                    <td className="audit__tool">{e.tool}</td>
                    <td className="audit__summary" title={e.summary}>
                      {e.summary}
                    </td>
                    <td>
                      <span className={decisionClass(e.decision)}>{e.decision}</span>
                    </td>
                    <td className={e.status === "error" ? "audit__err" : ""} title={e.detail}>
                      {e.status === "ok" ? <Check size={13} /> : e.status}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}
