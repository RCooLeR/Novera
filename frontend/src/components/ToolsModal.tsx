import { useEffect, useState } from "react";
import { Download, Scissors, X } from "lucide-react";
import { useStore } from "../state/store";

// Shows the result of a Tools-menu utility (CSV schema, CSV→SQL, SQL-dump
// analysis) and offers the write actions (save SQL, extract/split a dump).
export default function ToolsModal() {
  const result = useStore((s) => s.toolResult);
  const busy = useStore((s) => s.toolBusy);
  const close = useStore((s) => s.closeToolResult);
  const saveCsvToSql = useStore((s) => s.saveCsvToSql);
  const extractDumpTable = useStore((s) => s.extractDumpTable);
  const dumpTableToCsv = useStore((s) => s.dumpTableToCsv);
  const splitDump = useStore((s) => s.splitDump);
  const exportCsvColumns = useStore((s) => s.exportCsvColumns);
  const addCsvColumn = useStore((s) => s.addCsvColumn);

  // Editable inputs for the CSV→SQL flow + selected columns for schema export.
  const [table, setTable] = useState("");
  const [out, setOut] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [newCol, setNewCol] = useState("");
  const [newVal, setNewVal] = useState("");
  useEffect(() => {
    if (result?.kind === "sql") {
      setTable(result.table);
      setOut(result.out);
    } else if (result?.kind === "schema") {
      setSelected(result.schema.columns.map((c) => c.name));
    }
  }, [result]);

  const toggleCol = (name: string) =>
    setSelected((prev) => (prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]));

  useEffect(() => {
    if (!result) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        close();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [result, close]);

  if (!result) return null;

  return (
    <div className="modal-overlay" onMouseDown={close}>
      <div className="modal toolsmodal" role="dialog" aria-modal="true" aria-label={result.title} onMouseDown={(e) => e.stopPropagation()}>
        <div className="toolsmodal__head">
          <span className="modal__title">{result.title}</span>
          <button className="icon-btn" title="Close" aria-label="Close" onClick={close}>
            <X size={15} aria-hidden focusable={false} />
          </button>
        </div>
        <div className="toolsmodal__body">
          {result.kind === "error" && <div className="dbq__error">{result.message}</div>}

          {result.kind === "schema" && (
            <>
              <div className="toolsmodal__meta">
                {result.schema.columns.length} columns · sampled {result.schema.rowsScanned.toLocaleString()} row
                {result.schema.rowsScanned === 1 ? "" : "s"}
                {result.schema.truncated ? "+" : ""}
                <button
                  className="btn toolsmodal__splitbtn"
                  disabled={busy || selected.length === 0}
                  title="Write a new CSV keeping only the checked columns"
                  onClick={() =>
                    void exportCsvColumns(
                      result.rel,
                      result.schema.columns.map((c) => c.name).filter((n) => selected.includes(n)),
                    )
                  }
                >
                  Export {selected.length} columns →
                </button>
              </div>
              <table className="toolsmodal__table">
                <thead>
                  <tr>
                    <th></th>
                    <th>Column</th>
                    <th>Type</th>
                    <th>Nulls</th>
                    <th>Examples</th>
                  </tr>
                </thead>
                <tbody>
                  {result.schema.columns.map((c, i) => (
                    <tr key={i}>
                      <td>
                        <input
                          type="checkbox"
                          checked={selected.includes(c.name)}
                          aria-label={`Include ${c.name}`}
                          onChange={() => toggleCol(c.name)}
                        />
                      </td>
                      <td>{c.name}</td>
                      <td className="toolsmodal__type">{c.type}</td>
                      <td>{c.null.toLocaleString()}</td>
                      <td className="toolsmodal__samples">{c.samples.join(", ")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <div className="toolsmodal__form">
                <label>
                  Add column
                  <input value={newCol} spellCheck={false} placeholder="name" onChange={(e) => setNewCol(e.target.value)} />
                </label>
                <label>
                  Constant value
                  <input value={newVal} spellCheck={false} placeholder="(blank ok)" onChange={(e) => setNewVal(e.target.value)} />
                </label>
                <button
                  className="btn"
                  disabled={busy || !newCol.trim()}
                  title="Write a new CSV with this constant column appended"
                  onClick={() => void addCsvColumn(result.rel, newCol, newVal)}
                >
                  Add column →
                </button>
              </div>
            </>
          )}

          {result.kind === "dump" && (
            <>
              <div className="toolsmodal__meta">
                {result.dump.tables.length} table{result.dump.tables.length === 1 ? "" : "s"} · {result.dump.createTables}{" "}
                CREATE · {result.dump.insertTables} INSERT
                {result.dump.copyBlocks > 0 ? ` · ${result.dump.copyBlocks} COPY` : ""}
                {result.dump.mysqldump ? " · mysqldump" : ""}
                {result.dump.truncated ? " · scan capped" : ""}
                {result.dump.tables.length > 0 && (
                  <button className="btn toolsmodal__splitbtn" disabled={busy} onClick={() => void splitDump(result.rel)}>
                    <Scissors size={13} /> Split all to files
                  </button>
                )}
              </div>
              <table className="toolsmodal__table">
                <thead>
                  <tr>
                    <th>Table</th>
                    <th>CREATE</th>
                    <th>INSERT</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {result.dump.tables.map((t, i) => (
                    <tr key={i}>
                      <td>{t.name}</td>
                      <td>{t.createOffset >= 0 ? "✓" : "—"}</td>
                      <td>{t.insertOffset >= 0 ? "✓" : "—"}</td>
                      <td className="toolsmodal__rowactions">
                        <button className="btn btn--link" disabled={busy} title="Copy this table's statements to a .sql file" onClick={() => void extractDumpTable(result.rel, t.name)}>
                          .sql
                        </button>
                        <button className="btn btn--link" disabled={busy} title="Extract this table's COPY data to a .csv file (pg_dump)" onClick={() => void dumpTableToCsv(result.rel, t.name)}>
                          .csv
                        </button>
                      </td>
                    </tr>
                  ))}
                  {result.dump.tables.length === 0 && (
                    <tr>
                      <td colSpan={4} className="dbq__empty">
                        No tables found.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </>
          )}

          {result.kind === "sql" && (
            <>
              <div className="toolsmodal__form">
                <label>
                  Table
                  <input value={table} spellCheck={false} onChange={(e) => setTable(e.target.value)} />
                </label>
                <label>
                  Output file
                  <input value={out} spellCheck={false} onChange={(e) => setOut(e.target.value)} />
                </label>
                <button
                  className="btn btn--primary"
                  disabled={busy || !table.trim() || !out.trim()}
                  onClick={() => void saveCsvToSql(result.rel, out.trim(), table.trim())}
                >
                  <Download size={13} /> Save .sql
                </button>
              </div>
              <div className="toolsmodal__meta">Preview (sample of the first rows):</div>
              <pre className="toolsmodal__sql">{result.sql}</pre>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
