import { useEffect, useState } from "react";
import { CheckCircle2, CircleSlash, Loader2, Square, Trash2, XCircle } from "lucide-react";
import { useStore } from "../state/store";
import { Jobs } from "../lib/services";
import type { Job } from "../lib/services";

function StatusIcon({ status }: { status: string }) {
  switch (status) {
    case "running":
      return <Loader2 size={13} className="spin" style={{ color: "var(--accent)" }} />;
    case "success":
      return <CheckCircle2 size={13} style={{ color: "var(--green)" }} />;
    case "canceled":
      return <CircleSlash size={13} style={{ color: "var(--text-faint)" }} />;
    case "timeout":
      return <XCircle size={13} style={{ color: "var(--amber)" }} />;
    default:
      return <XCircle size={13} style={{ color: "var(--red)" }} />;
  }
}

function duration(j: Job): string {
  const end = j.endedAt || Date.now();
  const ms = Math.max(0, end - j.startedAt);
  if (ms < 1000) return `${ms}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

export default function JobsView() {
  const jobs = useStore((s) => s.jobs);
  const loadJobs = useStore((s) => s.loadJobs);
  const cancelJob = useStore((s) => s.cancelJob);
  const clearFinishedJobs = useStore((s) => s.clearFinishedJobs);
  const [openId, setOpenId] = useState<string | null>(null);
  const [log, setLog] = useState<string[]>([]);

  useEffect(() => {
    void loadJobs();
  }, [loadJobs]);

  // Load the selected job's log; refresh it whenever the jobs list changes (so a
  // running job's log keeps growing in the open panel).
  useEffect(() => {
    if (!openId) return;
    let alive = true;
    void Jobs.GetJob(openId)
      .then((j) => {
        if (alive) setLog(j.log ?? []);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [openId, jobs]);

  const hasFinished = jobs.some((j) => j.status !== "running");

  return (
    <div className="jobs">
      <div className="jobs__bar">
        <span className="jobs__count">
          {jobs.filter((j) => j.status === "running").length} running · {jobs.length} total
        </span>
        <span style={{ flex: 1 }} />
        <button className="icon-btn" title="Clear finished" disabled={!hasFinished} onClick={() => void clearFinishedJobs()}>
          <Trash2 size={13} />
        </button>
      </div>
      {jobs.length === 0 ? (
        <div className="jobs__empty">No background jobs yet. Agent runs appear here.</div>
      ) : (
        <div className="jobs__list">
          {jobs.map((j) => (
            <div key={j.id} className="jobs__item">
              <button
                className="jobs__row"
                onClick={() => setOpenId((id) => (id === j.id ? null : j.id))}
                title="Show log"
              >
                <StatusIcon status={j.status} />
                <span className="jobs__kind">{j.kind}</span>
                <span className="jobs__title">{j.title || "(untitled)"}</span>
                <span className="jobs__dur">{duration(j)}</span>
              </button>
              {j.status === "running" && (
                <button className="icon-btn jobs__cancel" title="Cancel job" onClick={() => void cancelJob(j.id)}>
                  <Square size={12} />
                </button>
              )}
              {openId === j.id && (
                <div className="jobs__detail">
                  {j.error && <div className="jobs__error">{j.error}</div>}
                  {log.length === 0 ? (
                    <div className="jobs__logempty">No log output.</div>
                  ) : (
                    <pre className="jobs__log">{log.join("\n")}</pre>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
