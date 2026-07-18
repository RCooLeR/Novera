import { useEffect, useState } from "react";
import { CheckCircle2, CircleSlash, Loader2, Square, Trash2, XCircle } from "lucide-react";
import { useStore } from "../state/store";
import { Jobs } from "../lib/services";
import { durationAt, visibleJobLog } from "./jobsViewState";
import type { JobLogState } from "./jobsViewState";

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

export default function JobsView() {
  const jobs = useStore((s) => s.jobs);
  const loadJobs = useStore((s) => s.loadJobs);
  const cancelJob = useStore((s) => s.cancelJob);
  const clearFinishedJobs = useStore((s) => s.clearFinishedJobs);
  const [openId, setOpenId] = useState<string | null>(null);
  const [logState, setLogState] = useState<JobLogState>({ jobId: "", lines: [], loading: false, error: "" });
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    void loadJobs();
  }, [loadJobs]);

  // Load the selected job's log; refresh it whenever the jobs list changes (so a
  // running job's log keeps growing in the open panel).
  useEffect(() => {
    if (!openId) {
      setLogState({ jobId: "", lines: [], loading: false, error: "" });
      return;
    }
    let alive = true;
    setLogState((current) => ({
      jobId: openId,
      lines: current.jobId === openId ? current.lines : [],
      loading: true,
      error: "",
    }));
    void Jobs.GetJob(openId)
      .then((j) => {
        if (alive) setLogState({ jobId: openId, lines: j.log ?? [], loading: false, error: "" });
      })
      .catch((error) => {
        if (alive) {
          setLogState({
            jobId: openId,
            lines: [],
            loading: false,
            error: `Could not load job log: ${String(error)}`,
          });
        }
      });
    return () => {
      alive = false;
    };
  }, [openId, jobs]);

  const hasRunning = jobs.some((j) => j.status === "running");
  useEffect(() => {
    if (!hasRunning) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [hasRunning]);

  const hasFinished = jobs.some((j) => j.status !== "running");
  const selectedLog = openId ? visibleJobLog(openId, logState) : null;

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
                <span className="jobs__dur">{durationAt(j, now)}</span>
              </button>
              {j.status === "running" && (
                <button className="icon-btn jobs__cancel" title="Cancel job" onClick={() => void cancelJob(j.id)}>
                  <Square size={12} />
                </button>
              )}
              {openId === j.id && selectedLog && (
                <div className="jobs__detail" aria-busy={selectedLog.loading}>
                  {j.error && <div className="jobs__error">{j.error}</div>}
                  {selectedLog.error && (
                    <div className="jobs__error" role="alert">
                      {selectedLog.error}
                    </div>
                  )}
                  {selectedLog.loading && selectedLog.lines.length === 0 ? (
                    <div className="jobs__logempty">Loading log...</div>
                  ) : selectedLog.lines.length === 0 ? (
                    <div className="jobs__logempty">No log output.</div>
                  ) : (
                    <pre className="jobs__log">{selectedLog.lines.join("\n")}</pre>
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
