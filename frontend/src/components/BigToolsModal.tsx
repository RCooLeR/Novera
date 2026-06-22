import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { BigFile, errMessage } from "../lib/services";
import type { CsvProfileResult, SqlLintResult, TransformResult } from "../lib/services";
import { useStore } from "../state/store";

/* ---------------------------------------------------------------------------
 * BigToolsModal — the ported Quarry CSV/SQL toolset, surfaced for the active
 * file. Opens the file in the bigfile engine (a fresh session id, independent
 * of any viewer/editor tab), inspects it (CSV delimiter/columns or SQL tables),
 * and drives the bound *ViaDialog transforms — each writes a NEW file to a
 * path the user picks; the source is never mutated.
 * ------------------------------------------------------------------------- */

const DELIMS = [
  { label: "Comma ,", val: "," },
  { label: "Tab ⇥", val: "\t" },
  { label: "Semicolon ;", val: ";" },
  { label: "Pipe |", val: "|" },
];
const FILTER_OPS = ["contains", "eq", "ne", "gt", "lt", "empty", "nonempty"];
const REDACT_MODES = ["null", "fixed", "hash", "email"];

function baseName(p: string): string {
  return p.replace(/\\/g, "/").split("/").filter(Boolean).pop() ?? p;
}

export default function BigToolsModal() {
  const target = useStore((s) => s.dataTools);
  const close = useStore((s) => s.closeDataTools);
  const root = useStore((s) => s.root);
  const setStatus = useStore((s) => s.setStatus);
  const refreshTree = useStore((s) => s.refreshTree);

  const rel = target?.rel ?? "";
  const isCsv = /\.(csv|tsv)$/i.test(rel);

  const [fileId, setFileId] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState("");

  // CSV context
  const [delim, setDelim] = useState(",");
  const [hasHeader, setHasHeader] = useState(true);
  const [columns, setColumns] = useState<string[]>([]);
  // SQL context
  const [tables, setTables] = useState<string[]>([]);
  const [presets, setPresets] = useState<string[]>([]);

  // CSV tool params
  const [fCol, setFCol] = useState(0);
  const [fOp, setFOp] = useState("contains");
  const [fVal, setFVal] = useState("");
  const [fNeg, setFNeg] = useState(false);
  const [dedupeKey, setDedupeKey] = useState(-1);
  const [everyN, setEveryN] = useState(10);
  const [keep, setKeep] = useState<Set<number>>(new Set());
  const [redact, setRedact] = useState<Set<number>>(new Set());
  const [redactMode, setRedactMode] = useState("null");
  const [redactRepl, setRedactRepl] = useState("");
  const [sqlTable, setSqlTableName] = useState("data");
  const [includeCreate, setIncludeCreate] = useState(true);
  const [numberKeys, setNumberKeys] = useState(false);
  const [typedCells, setTypedCells] = useState(true);
  const [sheetName, setSheetName] = useState("Sheet1");
  const [profile, setProfile] = useState<CsvProfileResult | null>(null);

  // SQL tool params
  const [table, setTable] = useState("");
  const [reshapeMode, setReshapeMode] = useState("single");
  const [batchSize, setBatchSize] = useState(500);
  const [fixtureRows, setFixtureRows] = useState(50);
  const [findStr, setFindStr] = useState("");
  const [replStr, setReplStr] = useState("");
  const [reRegex, setReRegex] = useState(false);
  const [reCase, setReCase] = useState(false);
  const [reWhole, setReWhole] = useState(false);
  const [preset, setPreset] = useState("");
  const [presetArgs, setPresetArgs] = useState(["", "", "", ""]);
  const [lint, setLint] = useState<SqlLintResult | null>(null);

  // Harvest
  const [harvestPat, setHarvestPat] = useState("");
  const [harvestCi, setHarvestCi] = useState(true);

  useEffect(() => {
    if (!target) return;
    const abs = root ? `${root}/${target.rel}` : target.rel;
    const csv = /\.(csv|tsv)$/i.test(target.rel);
    let openedId = "";
    let alive = true;
    setLoading(true);
    setErr("");
    BigFile.OpenFile(abs)
      .then(async (m) => {
        openedId = m.fileId;
        if (!alive) return;
        setFileId(m.fileId);
        if (csv) {
          const ins = await BigFile.CsvInspect(m.fileId);
          if (!alive) return;
          const d = ins.delimiter || ",";
          setDelim(d);
          setHasHeader(ins.hasHeader);
          const sch = await BigFile.CsvSchema(m.fileId, d, ins.hasHeader);
          if (!alive) return;
          const names = sch.columns.map((c) => c.name);
          setColumns(names);
          setKeep(new Set(names.map((_, i) => i)));
        } else {
          const an = await BigFile.SqlAnalyze(m.fileId);
          if (!alive) return;
          const names = an.tables.map((t) => t.name);
          setTables(names);
          if (names[0]) setTable(names[0]);
          setPresets(await BigFile.SqlListPresets());
        }
      })
      .catch((e) => alive && setErr(errMessage(e)))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
      if (openedId) void BigFile.CloseFile(openedId);
    };
  }, [target, root]);

  if (!target) return null;

  // Run a transform that returns a TransformResult (write-to-chosen-path).
  const run = async (label: string, fn: () => Promise<TransformResult>) => {
    setBusy(true);
    setErr("");
    setResult("");
    try {
      const r = await fn();
      if (r.outputPath) {
        const extra = [r.note, r.recordsWritten ? `${Number(r.recordsWritten).toLocaleString()} records` : ""].filter(Boolean);
        setResult(`${label} → ${r.outputPath}${extra.length ? " · " + extra.join(" · ") : ""}`);
        setStatus(`${label} → ${r.outputPath}`, "success");
        void refreshTree();
      } else {
        setResult(`${label}: cancelled`);
      }
    } catch (e) {
      setErr(errMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const toggle = (set: Set<number>, i: number, setter: (s: Set<number>) => void) => {
    const next = new Set(set);
    if (next.has(i)) next.delete(i);
    else next.add(i);
    setter(next);
  };

  const colOptions = columns.map((c, i) => (
    <option key={i} value={i}>
      {c || `col ${i + 1}`}
    </option>
  ));

  return (
    <div className="modal-overlay" onClick={close}>
      <div className="modal bigtools" onClick={(e) => e.stopPropagation()}>
        <div className="bigtools__head">
          <div className="modal__title">Data tools · {baseName(rel)}</div>
          {loading && <Loader2 size={15} className="spin" />}
        </div>

        {!loading && isCsv && (
          <div className="bigtools__ctx">
            <label>
              Delimiter
              <select value={delim} onChange={(e) => setDelim(e.target.value)}>
                {DELIMS.map((d) => (
                  <option key={d.label} value={d.val}>
                    {d.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="bigtools__check">
              <input type="checkbox" checked={hasHeader} onChange={(e) => setHasHeader(e.target.checked)} /> Header row
            </label>
            <span className="bigtools__dim">{columns.length} columns</span>
          </div>
        )}
        {!loading && !isCsv && <div className="bigtools__ctx bigtools__dim">{tables.length} tables discovered</div>}

        <div className="bigtools__body">
          {isCsv ? (
            <>
              <div className="bigtools__tool">
                <b>Filter rows</b>
                <div className="bigtools__row">
                  <select value={fCol} onChange={(e) => setFCol(+e.target.value)}>{colOptions}</select>
                  <select value={fOp} onChange={(e) => setFOp(e.target.value)}>
                    {FILTER_OPS.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </select>
                  {fOp !== "empty" && fOp !== "nonempty" && (
                    <input value={fVal} onChange={(e) => setFVal(e.target.value)} placeholder="value" />
                  )}
                  <label className="bigtools__check">
                    <input type="checkbox" checked={fNeg} onChange={(e) => setFNeg(e.target.checked)} /> negate
                  </label>
                  <button className="btn" disabled={busy} onClick={() => void run("Filter", () => BigFile.CsvFilterViaDialog(fileId, delim, hasHeader, fCol, fOp, fVal, fNeg))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Dedupe rows</b>
                <div className="bigtools__row">
                  <select value={dedupeKey} onChange={(e) => setDedupeKey(+e.target.value)}>
                    <option value={-1}>Whole row</option>
                    {colOptions}
                  </select>
                  <button className="btn" disabled={busy} onClick={() => void run("Dedupe", () => BigFile.CsvDedupeViaDialog(fileId, delim, hasHeader, dedupeKey))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Sample (every Nth row)</b>
                <div className="bigtools__row">
                  <input type="number" min={1} value={everyN} onChange={(e) => setEveryN(Math.max(1, +e.target.value))} style={{ width: 70 }} />
                  <button className="btn" disabled={busy} onClick={() => void run("Sample", () => BigFile.CsvSampleViaDialog(fileId, delim, hasHeader, everyN))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Keep columns</b>
                <div className="bigtools__cols">
                  {columns.map((c, i) => (
                    <label key={i} className="bigtools__check">
                      <input type="checkbox" checked={keep.has(i)} onChange={() => toggle(keep, i, setKeep)} /> {c || `col ${i + 1}`}
                    </label>
                  ))}
                </div>
                <button className="btn" disabled={busy} onClick={() => void run("Keep columns", () => BigFile.CsvProjectViaDialog(fileId, delim, [...keep].sort((a, b) => a - b)))}>
                  Write projected CSV
                </button>
              </div>

              <div className="bigtools__tool">
                <b>Redact / anonymise</b>
                <div className="bigtools__cols">
                  {columns.map((c, i) => (
                    <label key={i} className="bigtools__check">
                      <input type="checkbox" checked={redact.has(i)} onChange={() => toggle(redact, i, setRedact)} /> {c || `col ${i + 1}`}
                    </label>
                  ))}
                </div>
                <div className="bigtools__row">
                  <select value={redactMode} onChange={(e) => setRedactMode(e.target.value)}>
                    {REDACT_MODES.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </select>
                  {redactMode === "fixed" && <input value={redactRepl} onChange={(e) => setRedactRepl(e.target.value)} placeholder="replacement" />}
                  <button
                    className="btn"
                    disabled={busy || redact.size === 0}
                    onClick={() =>
                      void run("Redact", () =>
                        BigFile.CsvRedactViaDialog(
                          fileId,
                          delim,
                          hasHeader,
                          [...redact].sort((a, b) => a - b).map((i) => ({ index: i, mode: redactMode })),
                          redactRepl,
                        ),
                      )
                    }
                  >
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>CSV → SQL</b>
                <div className="bigtools__row">
                  <input value={sqlTable} onChange={(e) => setSqlTableName(e.target.value)} placeholder="table name" />
                  <label className="bigtools__check">
                    <input type="checkbox" checked={includeCreate} onChange={(e) => setIncludeCreate(e.target.checked)} /> CREATE TABLE
                  </label>
                  <button className="btn" disabled={busy} onClick={() => void run("CSV→SQL", () => BigFile.CsvToSQLViaDialog(fileId, delim, sqlTable, hasHeader, includeCreate))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Export</b>
                <div className="bigtools__row">
                  <label className="bigtools__check">
                    <input type="checkbox" checked={typedCells} onChange={(e) => setTypedCells(e.target.checked)} /> typed cells
                  </label>
                  <label className="bigtools__check">
                    <input type="checkbox" checked={numberKeys} onChange={(e) => setNumberKeys(e.target.checked)} /> numeric JSON keys
                  </label>
                  <input value={sheetName} onChange={(e) => setSheetName(e.target.value)} placeholder="sheet/table" style={{ width: 110 }} />
                </div>
                <div className="bigtools__row">
                  <button className="btn" disabled={busy} onClick={() => void run("Export JSONL", () => BigFile.CsvExportJSONLViaDialog(fileId, delim, hasHeader, numberKeys))}>
                    JSONL
                  </button>
                  <button className="btn" disabled={busy} onClick={() => void run("Export SQLite", () => BigFile.CsvExportSQLiteViaDialog(fileId, delim, hasHeader, sheetName, typedCells))}>
                    SQLite
                  </button>
                  <button className="btn" disabled={busy} onClick={() => void run("Export XLSX", () => BigFile.CsvExportXLSXViaDialog(fileId, delim, hasHeader, sheetName, typedCells))}>
                    XLSX
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Profile columns</b>
                <button
                  className="btn"
                  disabled={busy}
                  onClick={async () => {
                    setBusy(true);
                    setErr("");
                    try {
                      setProfile(await BigFile.CsvProfile(fileId, delim, hasHeader));
                    } catch (e) {
                      setErr(errMessage(e));
                    } finally {
                      setBusy(false);
                    }
                  }}
                >
                  Profile
                </button>
                {profile && (
                  <table className="bigtools__table">
                    <thead>
                      <tr>
                        <th>column</th>
                        <th>type</th>
                        <th>null</th>
                        <th>distinct</th>
                        <th>min</th>
                        <th>max</th>
                      </tr>
                    </thead>
                    <tbody>
                      {profile.columns.map((c, i) => (
                        <tr key={i}>
                          <td>{c.name}</td>
                          <td>{c.sqlType}</td>
                          <td>{Number(c.null).toLocaleString()}</td>
                          <td>
                            {Number(c.distinct).toLocaleString()}
                            {c.distinctCapped ? "+" : ""}
                          </td>
                          <td>{c.min}</td>
                          <td>{c.max}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            </>
          ) : (
            <>
              <div className="bigtools__tool">
                <b>Lint dump</b>
                <button
                  className="btn"
                  disabled={busy}
                  onClick={async () => {
                    setBusy(true);
                    setErr("");
                    try {
                      setLint(await BigFile.SqlLint(fileId));
                    } catch (e) {
                      setErr(errMessage(e));
                    } finally {
                      setBusy(false);
                    }
                  }}
                >
                  Lint
                </button>
                {lint && (
                  <ul className="bigtools__lint">
                    {lint.findings.length === 0 && <li className="bigtools__dim">No issues found.</li>}
                    {lint.findings.map((f, i) => (
                      <li key={i} className={f.severity === "warn" ? "bigtools__warn" : ""}>
                        <b>{f.title}</b> — {f.detail}
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div className="bigtools__tool">
                <b>Extract table</b>
                <div className="bigtools__row">
                  <select value={table} onChange={(e) => setTable(e.target.value)}>
                    {tables.map((t) => (
                      <option key={t} value={t}>
                        {t}
                      </option>
                    ))}
                  </select>
                  <button className="btn" disabled={busy || !table} onClick={() => void run("Extract table", () => BigFile.SqlExtractTableViaDialog(fileId, table))}>
                    Whole
                  </button>
                  <button className="btn" disabled={busy || !table} onClick={() => void run("Extract schema", () => BigFile.SqlExtractSchemaViaDialog(fileId, table))}>
                    Schema
                  </button>
                  <button className="btn" disabled={busy || !table} onClick={() => void run("Extract data", () => BigFile.SqlExtractDataViaDialog(fileId, table))}>
                    Data
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Split by table</b>
                <button className="btn" disabled={busy} onClick={() => void run("Split by table", () => BigFile.SqlSplitByTableViaDialog(fileId))}>
                  Write one file per table
                </button>
              </div>

              <div className="bigtools__tool">
                <b>Reshape INSERTs</b>
                <div className="bigtools__row">
                  <select value={reshapeMode} onChange={(e) => setReshapeMode(e.target.value)}>
                    <option value="single">single (one row / INSERT)</option>
                    <option value="multi">multi (batched)</option>
                  </select>
                  {reshapeMode === "multi" && (
                    <input type="number" min={1} value={batchSize} onChange={(e) => setBatchSize(Math.max(1, +e.target.value))} style={{ width: 80 }} />
                  )}
                  <button className="btn" disabled={busy} onClick={() => void run("Reshape", () => BigFile.SqlReshapeInsertsViaDialog(fileId, reshapeMode, batchSize))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Sample fixture</b>
                <div className="bigtools__row">
                  <input type="number" min={1} value={fixtureRows} onChange={(e) => setFixtureRows(Math.max(1, +e.target.value))} style={{ width: 80 }} /> rows / table
                  <button className="btn" disabled={busy} onClick={() => void run("Fixture", () => BigFile.SqlSampleFixtureViaDialog(fileId, fixtureRows))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Find &amp; replace</b>
                <div className="bigtools__row">
                  <input value={findStr} onChange={(e) => setFindStr(e.target.value)} placeholder="find" />
                  <input value={replStr} onChange={(e) => setReplStr(e.target.value)} placeholder="replace" />
                </div>
                <div className="bigtools__row">
                  <label className="bigtools__check">
                    <input type="checkbox" checked={reRegex} onChange={(e) => setReRegex(e.target.checked)} /> regex
                  </label>
                  <label className="bigtools__check">
                    <input type="checkbox" checked={reCase} onChange={(e) => setReCase(e.target.checked)} /> ignore case
                  </label>
                  <label className="bigtools__check">
                    <input type="checkbox" checked={reWhole} onChange={(e) => setReWhole(e.target.checked)} /> whole word
                  </label>
                  <button className="btn" disabled={busy || !findStr} onClick={() => void run("Replace", () => BigFile.SqlReplaceViaDialog(fileId, findStr, replStr, reRegex, reCase, reWhole))}>
                    Run
                  </button>
                </div>
              </div>

              {presets.length > 0 && (
                <div className="bigtools__tool">
                  <b>Cleanup preset</b>
                  <div className="bigtools__row">
                    <select value={preset} onChange={(e) => setPreset(e.target.value)}>
                      <option value="">Select…</option>
                      {presets.map((p) => (
                        <option key={p} value={p}>
                          {p}
                        </option>
                      ))}
                    </select>
                    {presetArgs.map((a, i) => (
                      <input
                        key={i}
                        value={a}
                        onChange={(e) => setPresetArgs((prev) => prev.map((v, j) => (j === i ? e.target.value : v)))}
                        placeholder={`arg ${i + 1}`}
                        style={{ width: 90 }}
                      />
                    ))}
                    <button className="btn" disabled={busy || !preset} onClick={() => void run("Preset", () => BigFile.SqlApplyPresetViaDialog(fileId, preset, presetArgs[0], presetArgs[1], presetArgs[2], presetArgs[3]))}>
                      Run
                    </button>
                  </div>
                </div>
              )}
            </>
          )}

          <div className="bigtools__tool">
            <b>Harvest matches (regex → one per line)</b>
            <div className="bigtools__row">
              <input value={harvestPat} onChange={(e) => setHarvestPat(e.target.value)} placeholder="regex pattern" />
              <label className="bigtools__check">
                <input type="checkbox" checked={harvestCi} onChange={(e) => setHarvestCi(e.target.checked)} /> ignore case
              </label>
              <button className="btn" disabled={busy || !harvestPat} onClick={() => void run("Harvest", () => BigFile.HarvestMatchesViaDialog(fileId, harvestPat, harvestCi))}>
                Run
              </button>
            </div>
          </div>
        </div>

        {err && <div className="bigtools__err">{err}</div>}
        {result && <div className="bigtools__ok">{result}</div>}

        <div className="modal__actions">
          {busy && <Loader2 size={14} className="spin" />}
          <button className="btn" onClick={close}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
