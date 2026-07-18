import { useEffect } from "react";
import { AlertTriangle, CircleAlert, Info, Loader2, RefreshCw } from "lucide-react";
import { useStore } from "../state/store";

function icon(severity: string) {
  if (severity === "error") return <CircleAlert size={14} color="var(--red)" />;
  if (severity === "warning") return <AlertTriangle size={14} color="var(--amber)" />;
  return <Info size={14} color="var(--text-muted)" />;
}

export default function ProblemsView() {
  const diagnostics = useStore((s) => s.diagnostics);
  const loading = useStore((s) => s.loadingDiagnostics);
  const runDiagnostics = useStore((s) => s.runDiagnostics);
  const openFileAt = useStore((s) => s.openFileAt);

  useEffect(() => {
    if (!diagnostics) void runDiagnostics();
  }, [diagnostics, runDiagnostics]);

  const items = diagnostics?.items ?? [];

  return (
    <div className="problems">
      <div className="problems__bar">
        <span className="problems__summary" role="status" aria-live="polite">
          {loading
            ? "Scanning…"
            : items.length === 0
              ? "No problems detected"
              : `${items.length} item${items.length === 1 ? "" : "s"} in ${diagnostics?.fileCount} file${diagnostics?.fileCount === 1 ? "" : "s"}${diagnostics?.truncated ? " (truncated)" : ""}`}
        </span>
        <button className="icon-btn" title="Rescan" onClick={() => void runDiagnostics()}>
          {loading ? <Loader2 size={14} className="spin" /> : <RefreshCw size={14} />}
        </button>
      </div>
      <div className="problems__list">
        {items.map((d, i) => (
          <button
            type="button"
            key={`${d.path}:${d.line}:${d.column}:${d.kind}:${i}`}
            className="problems__item"
            onClick={() => void openFileAt(d.path, d.line, d.column)}
            title={`${d.path}:${d.line}`}
            aria-label={`Open ${d.severity} ${d.kind} in ${d.path}, line ${d.line}: ${d.message}`}
          >
            {icon(d.severity)}
            <span className="problems__kind">{d.kind}</span>
            <span className="problems__msg">{d.message}</span>
            <span className="problems__loc">
              {d.path}:{d.line}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}
