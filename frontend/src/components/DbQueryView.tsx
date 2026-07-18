import { useMemo, useState } from "react";
import { Clipboard, Download, History, Loader2, Play } from "lucide-react";
import { Db, Shell, errMessage } from "../lib/services";
import type { DbQueryResult } from "../lib/services";
import { toCsv, sortIndices, nextSort, type Sort } from "../lib/grid";
import { useStore } from "../state/store";
import VirtualGrid from "./VirtualGrid";

function truncationLabel(reason: string): string {
  if (reason === "row_limit") return "row limit reached";
  if (reason === "byte_limit") return "result byte limit reached";
  return "truncated";
}

export default function DbQueryView({ connId }: { connId: string }) {
  const sql = useStore((s) => s.dbSql[connId] ?? "");
  const setDbSql = useStore((s) => s.setDbSql);
  const pushDbHistory = useStore((s) => s.pushDbHistory);
  const history = useStore((s) => s.dbHistory[connId] ?? []);
  const setStatus = useStore((s) => s.setStatus);
  const [result, setResult] = useState<DbQueryResult | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  const [limit, setLimit] = useState(1000);
  const [sort, setSort] = useState<Sort>(null);
  const [showHistory, setShowHistory] = useState(false);

  // Reorder rows and the NULL mask through one permutation so they stay aligned.
  const order = useMemo(() => (result ? sortIndices(result.rows, sort) : []), [result, sort]);
  const rows = useMemo(() => (result ? order.map((i) => result.rows[i]) : []), [order, result]);
  const nulls = useMemo(() => (result ? order.map((i) => result.nulls?.[i] ?? []) : []), [order, result]);

  const run = async () => {
    if (!sql.trim() || running) return;
    setRunning(true);
    setError("");
    setSort(null);
    try {
      const r = await Db.Query(connId, sql, limit);
      setResult(r);
      pushDbHistory(connId, sql);
    } catch (e) {
      setError(errMessage(e));
      setResult(null);
    } finally {
      setRunning(false);
    }
  };

  const copyCsv = () => {
    if (result) void navigator.clipboard?.writeText(toCsv(result.columns, rows));
  };

  const exportCsv = async () => {
    if (!result) return;
    try {
      const path = await Shell.SaveTextFile("query-results.csv", toCsv(result.columns, rows));
      if (path) setStatus(`Exported to ${path}`, "success");
    } catch (e) {
      setStatus(errMessage(e), "error");
    }
  };

  return (
    <div className="dbq">
      <div className="dbq__editor">
        <textarea
          className="dbq__sql"
          value={sql}
          spellCheck={false}
          placeholder="SELECT * FROM …   (Ctrl+Enter to run — read-only SELECT/WITH only)"
          onChange={(e) => setDbSql(connId, e.target.value)}
          onKeyDown={(e) => {
            if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
              e.preventDefault();
              void run();
            }
          }}
        />
        <div className="dbq__bar">
          <button className="btn btn--primary" onClick={() => void run()} disabled={running || !sql.trim()}>
            {running ? <Loader2 size={14} className="spin" /> : <Play size={14} />} Run
          </button>
          <select className="dbq__limit" value={limit} onChange={(e) => setLimit(Number(e.target.value))} title="Max rows">
            <option value={100}>100 rows</option>
            <option value={1000}>1000 rows</option>
            <option value={2500}>2500 rows</option>
            <option value={5000}>5000 rows</option>
          </select>
          <div className="dbq__histwrap">
            <button
              className="icon-btn"
              title="Query history"
              disabled={history.length === 0}
              onClick={() => setShowHistory((v) => !v)}
            >
              <History size={14} />
            </button>
            {showHistory && history.length > 0 && (
              <div className="dbq__history" onMouseLeave={() => setShowHistory(false)}>
                {history.map((q, i) => (
                  <button
                    key={i}
                    className="dbq__historyitem"
                    title={q}
                    onClick={() => {
                      setDbSql(connId, q);
                      setShowHistory(false);
                    }}
                  >
                    {q.replace(/\s+/g, " ").slice(0, 80)}
                  </button>
                ))}
              </div>
            )}
          </div>
          {result && (
            <span className="dbq__meta">
              {result.rowCount} row{result.rowCount === 1 ? "" : "s"}
              {result.truncated ? ` (${truncationLabel(result.truncationReason)})` : ""} · {result.elapsedMs} ms
            </span>
          )}
          {result && result.rows.length > 0 && (
            <>
              <button className="btn" onClick={copyCsv} title="Copy results as CSV">
                <Clipboard size={13} /> Copy
              </button>
              <button className="btn" onClick={() => void exportCsv()} title="Export results to a CSV file">
                <Download size={13} /> Export
              </button>
            </>
          )}
        </div>
      </div>

      <div className="dbq__results">
        {error && <div className="dbq__error">{error}</div>}
        {!error && result && (
          <VirtualGrid columns={result.columns} rows={rows} nulls={nulls} sort={sort} onSort={(i) => setSort((s) => nextSort(s, i))} />
        )}
        {!error && !result && <div className="dbq__hint">Run a query to see results.</div>}
      </div>
    </div>
  );
}
