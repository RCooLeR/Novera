import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { BigFile, errMessage } from "../lib/services";
import type { TransformResult } from "../lib/services";
import { useStore } from "../state/store";
import { useDialogFocus } from "../lib/useDialogFocus";
import { sqlReplaceUnavailableReason } from "../lib/sqlReplaceCapability";
import { bigFileJobProgressLabel } from "../lib/bigFileJobState";
import BigToolsCsvExportActions from "./BigToolsCsvExportActions";
import {
  BIG_TOOLS_COLUMN_DOM_LIMIT,
  BIG_TOOLS_IDENTIFIER_INPUT_LIMIT,
  BIG_TOOLS_SQL_ROW_LIMIT,
  BIG_TOOLS_VALUE_INPUT_LIMIT,
  BIG_TOOLS_WARNING_DOM_LIMIT,
  bigToolsSqlTableCapabilities,
  boundedBigToolsInteger,
  boundedBigToolsText,
  isValidBigToolsCsvDelimiter,
  normalizeBigToolsCsvInspect,
  normalizeBigToolsCsvProfile,
  normalizeBigToolsCsvSchema,
  normalizeBigToolsSqlLint,
  normalizeBigToolsSqlTables,
  pageMatchingStrings,
  type BigToolsCsvInspect,
  type BigToolsCsvProfile,
  type BigToolsSqlLint,
  type BigToolsSqlTable,
} from "../lib/bigToolsPresentation";
import {
  BigToolsRequestGate,
  bigToolsFileKind,
  bigToolsSqlAvailability,
  unsupportedBigToolsTypeMessage,
  type BigToolsFileKind,
} from "../lib/bigToolsOwnership";

/* ---------------------------------------------------------------------------
 * BigToolsModal — the ported Quarry CSV/SQL toolset, surfaced for the active
 * file. Opens the file in the bigfile engine (a fresh session id, independent
 * of any viewer/editor tab), inspects CSV delimiter/columns immediately, and
 * analyzes SQL tables only when requested. The bound *ViaDialog transforms each
 * write a NEW file to a path the user picks; the source is never mutated.
 * ------------------------------------------------------------------------- */

const DELIMS = [
  { label: "Comma ,", val: "," },
  { label: "Tab ⇥", val: "\t" },
  { label: "Semicolon ;", val: ";" },
  { label: "Pipe |", val: "|" },
  { label: "Space", val: " " },
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
  const activeJob = useStore((s) => s.bigFileJob);

  const rel = target?.rel ?? "";

  const [ownedTarget, setOwnedTarget] = useState<{ rel: string } | null>(null);
  const [ownedRoot, setOwnedRoot] = useState("");
  const [fileId, setFileId] = useState("");
  const [fileKind, setFileKind] = useState<BigToolsFileKind>("pending");
  const [ready, setReady] = useState(false);
  const [loading, setLoading] = useState(true);
  const [schemaLoading, setSchemaLoading] = useState(false);
  const [schemaReady, setSchemaReady] = useState(false);
  const [analysisLoading, setAnalysisLoading] = useState(false);
  const [analysisReady, setAnalysisReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [canceling, setCanceling] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState("");

  // CSV context
  const [delim, setDelim] = useState(",");
  const [hasHeader, setHasHeader] = useState(true);
  const [sourceGeneration, setSourceGeneration] = useState(0);
  const [columns, setColumns] = useState<string[]>([]);
  const [csvInspect, setCsvInspect] = useState<BigToolsCsvInspect | null>(null);
  const [schemaWarnings, setSchemaWarnings] = useState<string[]>([]);
  // SQL context
  const [tables, setTables] = useState<BigToolsSqlTable[]>([]);
  const [sqlReplaceReason, setSqlReplaceReason] = useState("");

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
  const [addColumnValue, setAddColumnValue] = useState("");
  const [sqlTable, setSqlTableName] = useState("data");
  const [includeCreate, setIncludeCreate] = useState(true);
  const [numberKeys, setNumberKeys] = useState(false);
  const [profile, setProfile] = useState<BigToolsCsvProfile | null>(null);

  // SQL tool params
  const [table, setTable] = useState("");
  const [tableFilter, setTableFilter] = useState("");
  const [tablePage, setTablePage] = useState(0);
  const [reshapeMode, setReshapeMode] = useState("single");
  const [batchSize, setBatchSize] = useState(500);
  const [fixtureRows, setFixtureRows] = useState(50);
  const [findStr, setFindStr] = useState("");
  const [replStr, setReplStr] = useState("");
  const [reCase, setReCase] = useState(false);
  const [reWhole, setReWhole] = useState(false);
  const [lint, setLint] = useState<BigToolsSqlLint | null>(null);

  // Harvest
  const [harvestPat, setHarvestPat] = useState("");
  const [harvestCi, setHarvestCi] = useState(true);
  const closeRef = useRef<HTMLButtonElement | null>(null);
  const requestGate = useRef(new BigToolsRequestGate());
  const activeJobRef = useRef(activeJob);
  const currentTargetRef = useRef(target);
  const currentRootRef = useRef(root);
  const currentCsvConfigurationRef = useRef("");
  const currentCsvGenerationRef = useRef(0);
  const loadedSchemaKey = useRef("");
  const csvConfigurationKey = `${delim}\0${hasHeader}`;
  const targetStateCurrent = ownedTarget === target && ownedRoot === root;
  const visibleFileKind = targetStateCurrent ? fileKind : "pending";
  const isCsv = visibleFileKind === "csv";
  const isSql = visibleFileKind === "sql";
  const activeTargetJob =
    targetStateCurrent && activeJob && fileId && activeJob.fileId === fileId ? activeJob : null;
  const jobBusy = (targetStateCurrent && (busy || analysisLoading)) || activeJob !== null;
  const closeBusy =
    (targetStateCurrent && (busy || analysisLoading)) || activeTargetJob !== null;
  const visibleColumns = columns.slice(0, BIG_TOOLS_COLUMN_DOM_LIMIT);
  const omittedColumns = columns.length - visibleColumns.length;
  const csvWarnings = useMemo(
    () => Array.from(new Set([...(csvInspect?.warnings ?? []), ...schemaWarnings])),
    [csvInspect, schemaWarnings],
  );
  const renderedCsvWarnings = csvWarnings.slice(0, BIG_TOOLS_WARNING_DOM_LIMIT);
  const omittedCsvWarnings = csvWarnings.length - renderedCsvWarnings.length;
  const delimiterOptions = useMemo(() => {
    const candidates = (csvInspect?.candidates ?? [])
      .filter((candidate) => isValidBigToolsCsvDelimiter(candidate.delimiter))
      .map((candidate) => ({
        val: candidate.delimiter,
        label: `${candidate.name || JSON.stringify(candidate.delimiter)} (${candidate.columns} columns, score ${candidate.score.toFixed(2)})`,
      }));
    const options = [...candidates, ...DELIMS].filter(
      (option, index, all) => all.findIndex((other) => other.val === option.val) === index,
    );
    if (isValidBigToolsCsvDelimiter(delim) && !options.some((option) => option.val === delim)) {
      options.unshift({ val: delim, label: `Configured ${JSON.stringify(delim)}` });
    }
    return options;
  }, [csvInspect, delim]);
  const tableNames = useMemo(() => tables.map((candidate) => candidate.name), [tables]);
  const tableWindow = useMemo(
    () => pageMatchingStrings(tableNames, tableFilter, tablePage),
    [tableFilter, tableNames, tablePage],
  );
  const selectedTableName = tableWindow.items.includes(table)
    ? table
    : (tableWindow.items[0] ?? "");
  const selectedTable = tables.find((candidate) => candidate.name === selectedTableName) ?? null;
  const selectedTableCapabilities = bigToolsSqlTableCapabilities(selectedTable);
  useLayoutEffect(() => {
    activeJobRef.current = activeJob;
    currentTargetRef.current = target;
    currentRootRef.current = root;
    currentCsvConfigurationRef.current = csvConfigurationKey;
    currentCsvGenerationRef.current = sourceGeneration;
  }, [activeJob, csvConfigurationKey, root, sourceGeneration, target]);
  const requestClose = useCallback(() => {
    if (!closeBusy) close();
  }, [closeBusy, close]);
  const dialogRef = useDialogFocus(!!target, requestClose, closeRef);

  useEffect(() => {
    if (!target) return;
    const abs = root ? `${root}/${target.rel}` : target.rel;
    const targetEpoch = requestGate.current.beginTarget();
    const requestedTarget = target;
    const requestedRoot = root;
    const ownsTarget = () =>
      requestGate.current.isTargetCurrent(targetEpoch) &&
      currentTargetRef.current === requestedTarget &&
      currentRootRef.current === requestedRoot;
    let openedId = "";
    loadedSchemaKey.current = "";
    setOwnedTarget(target);
    setOwnedRoot(root);
    setLoading(true);
    setSchemaLoading(false);
    setSchemaReady(false);
    setAnalysisLoading(false);
    setAnalysisReady(false);
    setReady(false);
    setFileKind("pending");
    setFileId("");
    setBusy(false);
    setCanceling(false);
    setErr("");
    setResult("");
    setColumns([]);
    setSourceGeneration(0);
    setCsvInspect(null);
    setSchemaWarnings([]);
    setTables([]);
    setTable("");
    setTableFilter("");
    setTablePage(0);
    setSqlReplaceReason("");
    setProfile(null);
    setLint(null);
    setKeep(new Set());
    setRedact(new Set());
    setAddColumnValue("");
    BigFile.OpenFile(abs)
      .then(async (m) => {
        openedId = m.fileId;
        if (!ownsTarget()) {
          void BigFile.CloseFile(m.fileId);
          return;
        }
        const detectedKind = bigToolsFileKind(m.detected);
        setFileKind(detectedKind);
        if (detectedKind === "unsupported") {
          setErr(unsupportedBigToolsTypeMessage(m.detected));
          return;
        }
        setFileId(m.fileId);
        if (detectedKind === "csv") {
          const ins = normalizeBigToolsCsvInspect(await BigFile.CsvInspect(m.fileId));
          if (!ownsTarget()) return;
          const d = ins.delimiter || ",";
          setSourceGeneration(ins.generation);
          setCsvInspect(ins);
          setDelim(d);
          setHasHeader(ins.hasHeader);
          const sch = normalizeBigToolsCsvSchema(
            await BigFile.CsvSchema(m.fileId, d, ins.hasHeader),
          );
          if (!ownsTarget()) return;
          if (sch.generation !== ins.generation) {
            throw new Error("CSV schema belongs to a different source generation. Reopen the tools.");
          }
          const names = sch.columns;
          loadedSchemaKey.current = `${d}\0${ins.hasHeader}`;
          setColumns(names);
          setSchemaWarnings(sch.warnings);
          setKeep(new Set(names.map((_, i) => i)));
          setSchemaReady(true);
          setReady(true);
        } else {
          setSqlReplaceReason(sqlReplaceUnavailableReason(m));
          setReady(true);
        }
      })
      .catch((e) => {
        if (ownsTarget()) setErr(errMessage(e));
      })
      .finally(() => {
        if (ownsTarget()) setLoading(false);
      });
    return () => {
      requestGate.current.invalidateTarget();
      if (openedId) void BigFile.CloseFile(openedId);
    };
  }, [target, root]);

  useEffect(() => {
    if (!target || !isCsv || !ready || !fileId || loading) return;
    const key = csvConfigurationKey;
    const expectedGeneration = sourceGeneration;
    const request = requestGate.current.begin("schema");
    const requestedTarget = target;
    const requestedRoot = root;
    const ownsRequest = () =>
      requestGate.current.isCurrent(request) &&
      currentTargetRef.current === requestedTarget &&
      currentRootRef.current === requestedRoot &&
      currentCsvConfigurationRef.current === key;
    setErr("");
    setResult("");
    setProfile(null);
    if (loadedSchemaKey.current === key) {
      setSchemaReady(true);
      setSchemaLoading(false);
      return;
    }
    loadedSchemaKey.current = "";
    setSchemaReady(false);
    setSchemaLoading(true);
    setColumns([]);
    setSchemaWarnings([]);
    setKeep(new Set());
    setRedact(new Set());
    void BigFile.CsvSchema(fileId, delim, hasHeader)
      .then((schema) => {
        if (!ownsRequest()) return;
        const normalized = normalizeBigToolsCsvSchema(schema);
        if (normalized.generation !== expectedGeneration) {
          throw new Error("CSV schema belongs to a stale source generation. Reopen the tools.");
        }
        const names = normalized.columns;
        loadedSchemaKey.current = key;
        setColumns(names);
        setSchemaWarnings(normalized.warnings);
        setFCol(0);
        setDedupeKey(-1);
        setKeep(new Set(names.map((_, index) => index)));
        setRedact(new Set());
        setSchemaReady(true);
      })
      .catch((schemaError) => {
        if (ownsRequest()) setErr(errMessage(schemaError));
      })
      .finally(() => {
        if (ownsRequest()) setSchemaLoading(false);
      });
  }, [csvConfigurationKey, delim, fileId, hasHeader, isCsv, loading, ready, root, sourceGeneration, target]);

  useEffect(() => {
    setCanceling(false);
  }, [activeTargetJob?.id]);

  const baseUnavailable =
    !targetStateCurrent ||
    jobBusy ||
    loading ||
    schemaLoading ||
    !ready ||
    (isCsv && (!schemaReady || loadedSchemaKey.current !== csvConfigurationKey)) ||
    (isCsv && sourceGeneration <= 0) ||
    !fileId ||
    (!isCsv && !isSql);
  const {
    independentUnavailable: unavailable,
    analysisDependentUnavailable,
  } = bigToolsSqlAvailability(baseUnavailable, analysisReady);

  if (!target) return null;

  const analyzeSql = async () => {
    if (unavailable || !isSql || analysisLoading) return;
    const request = requestGate.current.begin("analysis");
    const requestedTarget = target;
    const requestedRoot = root;
    const requestedFileId = fileId;
    const ownsRequest = () =>
      requestGate.current.isCurrent(request) &&
      currentTargetRef.current === requestedTarget &&
      currentRootRef.current === requestedRoot;
    setAnalysisLoading(true);
    setAnalysisReady(false);
    setTables([]);
    setTable("");
    setTableFilter("");
    setTablePage(0);
    setLint(null);
    setErr("");
    setResult("");
    try {
      const nextTables = normalizeBigToolsSqlTables(await BigFile.SqlAnalyze(requestedFileId));
      if (!ownsRequest()) return;
      setTables(nextTables);
      setTable(nextTables[0]?.name ?? "");
      setAnalysisReady(true);
      setResult(
        `Analysis complete: ${nextTables.length.toLocaleString()} table${
          nextTables.length === 1 ? "" : "s"
        } discovered.`,
      );
    } catch (analysisError) {
      if (ownsRequest()) {
        setErr(
          `SQL analysis failed: ${errMessage(
            analysisError,
          )} Find/replace, reshape, and match harvesting remain available.`,
        );
      }
    } finally {
      if (ownsRequest()) setAnalysisLoading(false);
    }
  };

  // Run a transform that returns a TransformResult (write-to-chosen-path).
  const run = async (
    label: string,
    fn: () => Promise<TransformResult>,
    requiresAnalysis = false,
  ) => {
    if (unavailable || (requiresAnalysis && analysisDependentUnavailable)) return;
    const request = requestGate.current.begin("transform");
    const requestedTarget = target;
    const requestedRoot = root;
    const requestedCsvConfiguration = currentCsvConfigurationRef.current;
    const requestedSourceGeneration = currentCsvGenerationRef.current;
    const usesCsvConfiguration = isCsv;
    const ownsRequest = () =>
      requestGate.current.isCurrent(request) &&
      currentTargetRef.current === requestedTarget &&
      currentRootRef.current === requestedRoot &&
      (!usesCsvConfiguration ||
        (currentCsvConfigurationRef.current === requestedCsvConfiguration &&
          currentCsvGenerationRef.current === requestedSourceGeneration));
    setBusy(true);
    setCanceling(false);
    setErr("");
    setResult("");
    try {
      const r = await fn();
      if (!ownsRequest()) return;
      if (r.outputPath) {
        const extra = [r.note, r.recordsWritten ? `${Number(r.recordsWritten).toLocaleString()} records` : ""].filter(Boolean);
        setResult(`${label} → ${r.outputPath}${extra.length ? " · " + extra.join(" · ") : ""}`);
        setStatus(`${label} → ${r.outputPath}`, "success");
        void refreshTree();
      } else {
        setResult(`${label}: cancelled`);
      }
    } catch (e) {
      if (ownsRequest()) setErr(errMessage(e));
    } finally {
      if (ownsRequest()) {
        setBusy(false);
        setCanceling(false);
      }
    }
  };

  const runInspection = async <T,>(
    channel: "profile" | "lint",
    load: () => Promise<T>,
    apply: (value: T) => void,
    requiresAnalysis = false,
  ) => {
    if (unavailable || (requiresAnalysis && analysisDependentUnavailable)) return;
    const request = requestGate.current.begin(channel);
    const requestedTarget = target;
    const requestedRoot = root;
    const requestedCsvConfiguration = currentCsvConfigurationRef.current;
    const requestedSourceGeneration = currentCsvGenerationRef.current;
    const ownsRequest = () =>
      requestGate.current.isCurrent(request) &&
      currentTargetRef.current === requestedTarget &&
      currentRootRef.current === requestedRoot &&
      (channel !== "profile" ||
        (currentCsvConfigurationRef.current === requestedCsvConfiguration &&
          currentCsvGenerationRef.current === requestedSourceGeneration));
    setBusy(true);
    setErr("");
    try {
      const value = await load();
      if (ownsRequest()) apply(value);
    } catch (inspectionError) {
      if (ownsRequest()) setErr(errMessage(inspectionError));
    } finally {
      if (ownsRequest()) setBusy(false);
    }
  };

  const cancelTransform = async () => {
    if (!activeTargetJob || canceling) return;
    const request = requestGate.current.begin("cancel");
    const requestedTarget = target;
    const requestedRoot = root;
    const ownsRequest = () =>
      requestGate.current.isCurrent(request) &&
      currentTargetRef.current === requestedTarget &&
      currentRootRef.current === requestedRoot;
    const jobID = activeTargetJob.id;
    const jobTitle = activeTargetJob.title;
    setCanceling(true);
    try {
      await BigFile.CancelJob(jobID);
      if (!ownsRequest()) return;
      const currentJob = activeJobRef.current;
      if (!currentJob || currentJob.id === jobID) setResult(`Cancelling ${jobTitle}...`);
    } catch (cancelError) {
      if (!ownsRequest()) return;
      const currentJob = activeJobRef.current;
      if (!currentJob || currentJob.id === jobID) setErr(errMessage(cancelError));
      setCanceling(false);
    }
  };

  const toggle = (set: Set<number>, i: number, setter: (s: Set<number>) => void) => {
    const next = new Set(set);
    if (next.has(i)) next.delete(i);
    else next.add(i);
    setter(next);
  };

  const colOptions = visibleColumns.map((c, i) => (
    <option key={i} value={i}>
      {c || `col ${i + 1}`}
    </option>
  ));

  return (
    <div className="modal-overlay" onClick={requestClose}>
      <div
        ref={dialogRef}
        className="modal bigtools"
        role="dialog"
        aria-modal="true"
        aria-labelledby="bigtools-title"
        aria-busy={loading || schemaLoading || analysisLoading || jobBusy}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="bigtools__head">
          <div id="bigtools-title" className="modal__title" role="heading" aria-level={2}>
            Data tools · {baseName(rel)}
          </div>
          {(loading || schemaLoading || analysisLoading) && (
            <Loader2 size={15} className="spin" aria-hidden="true" />
          )}
        </div>

        {!loading && isCsv && (
          <div className="bigtools__ctx">
            <label>
              Delimiter
              <select value={delim} disabled={busy || schemaLoading} onChange={(e) => setDelim(e.target.value)}>
                {delimiterOptions.map((d) => (
                  <option key={d.label} value={d.val}>
                    {d.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="bigtools__check">
              <input type="checkbox" checked={hasHeader} disabled={busy || schemaLoading} onChange={(e) => setHasHeader(e.target.checked)} /> Header row
            </label>
            <span className="bigtools__dim">{columns.length} columns</span>
          </div>
        )}
        {!loading && isSql && (
          <div className="bigtools__ctx bigtools__dim">
            {analysisReady
              ? `${tables.length.toLocaleString()} tables discovered`
              : "Analyze the dump to enable schema, extraction, split, lint, and fixture tools."}
          </div>
        )}
        {!loading && isCsv && csvInspect && csvInspect.candidates.length > 0 && (
          <div className="bigtools__hint" role="note">
            Ranked separator candidates:{" "}
            {csvInspect.candidates
              .map(
                (candidate) =>
                  `${candidate.name || JSON.stringify(candidate.delimiter)} (${candidate.columns} columns, score ${candidate.score.toFixed(2)})`,
              )
              .join("; ")}
          </div>
        )}
        {!loading && isCsv && csvWarnings.length > 0 && (
          <div className="bigtools__hint" role="status" aria-live="polite">
            <b>Bounded-sample warnings</b>
            <ul>
              {renderedCsvWarnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
            {omittedCsvWarnings > 0 && (
              <span>
                Showing the first {BIG_TOOLS_WARNING_DOM_LIMIT}; {omittedCsvWarnings} additional
                warnings are omitted.
              </span>
            )}
          </div>
        )}

        <div className="bigtools__body">
          {visibleFileKind === "csv" ? (
            <>
              <div className="bigtools__tool">
                <b>Filter rows</b>
                <div className="bigtools__row">
                  <select aria-label="Filter column" value={fCol} onChange={(e) => setFCol(+e.target.value)}>{colOptions}</select>
                  <select aria-label="Filter operation" value={fOp} onChange={(e) => setFOp(e.target.value)}>
                    {FILTER_OPS.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </select>
                  {fOp !== "empty" && fOp !== "nonempty" && (
                    <input
                      aria-label="Filter comparison value"
                      maxLength={BIG_TOOLS_VALUE_INPUT_LIMIT}
                      value={fVal}
                      onChange={(e) =>
                        setFVal(
                          boundedBigToolsText(e.target.value, BIG_TOOLS_VALUE_INPUT_LIMIT),
                        )
                      }
                      placeholder="value"
                    />
                  )}
                  <label className="bigtools__check">
                    <input type="checkbox" checked={fNeg} onChange={(e) => setFNeg(e.target.checked)} /> negate
                  </label>
                  <button className="btn" aria-label="Run CSV row filter" disabled={unavailable} onClick={() => void run("Filter", () => BigFile.CsvFilterViaDialog(fileId, sourceGeneration, delim, hasHeader, fCol, fOp, fVal, fNeg))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Dedupe rows</b>
                <div className="bigtools__row">
                  <select aria-label="Dedupe column" value={dedupeKey} onChange={(e) => setDedupeKey(+e.target.value)}>
                    <option value={-1}>Whole row</option>
                    {colOptions}
                  </select>
                  <button className="btn" aria-label="Run CSV deduplication" disabled={unavailable} onClick={() => void run("Dedupe", () => BigFile.CsvDedupeViaDialog(fileId, sourceGeneration, delim, hasHeader, dedupeKey))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Sample (every Nth row)</b>
                <div className="bigtools__row">
                  <input
                    aria-label="Sample every Nth row"
                    type="number"
                    min={2}
                    value={everyN}
                    onChange={(e) =>
                      setEveryN(
                        boundedBigToolsInteger(
                          e.target.value,
                          2,
                          Number.MAX_SAFE_INTEGER,
                          10,
                        ),
                      )
                    }
                    style={{ width: 70 }}
                  />
                  <button className="btn" aria-label="Run CSV sampling" disabled={unavailable} onClick={() => void run("Sample", () => BigFile.CsvSampleViaDialog(fileId, sourceGeneration, delim, hasHeader, everyN))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Keep columns</b>
                <div className="bigtools__cols">
                  {visibleColumns.map((c, i) => (
                    <label key={i} className="bigtools__check">
                      <input type="checkbox" checked={keep.has(i)} onChange={() => toggle(keep, i, setKeep)} /> {c || `col ${i + 1}`}
                    </label>
                  ))}
                </div>
                <button className="btn" disabled={unavailable} onClick={() => void run("Keep columns", () => BigFile.CsvProjectViaDialog(fileId, sourceGeneration, delim, [...keep].sort((a, b) => a - b)))}>
                  Write projected CSV
                </button>
                {omittedColumns > 0 && (
                  <p className="bigtools__hint" role="note">
                    Column controls materialize the first {BIG_TOOLS_COLUMN_DOM_LIMIT} columns;{" "}
                    {omittedColumns} additional columns remain kept by projection and excluded from
                    redaction.
                  </p>
                )}
              </div>

              <div className="bigtools__tool">
                <b>Add constant column</b>
                <div className="bigtools__row">
                  <input
                    aria-label="Constant value for added CSV column"
                    maxLength={BIG_TOOLS_VALUE_INPUT_LIMIT}
                    value={addColumnValue}
                    onChange={(event) =>
                      setAddColumnValue(
                        boundedBigToolsText(event.target.value, BIG_TOOLS_VALUE_INPUT_LIMIT),
                      )
                    }
                    placeholder="constant value"
                  />
                  <button
                    type="button"
                    className="btn"
                    aria-label="Add constant column to a new CSV file"
                    disabled={unavailable}
                    onClick={() =>
                      void run("Add column", () =>
                        BigFile.CsvAddColumnViaDialog(
                          fileId,
                          sourceGeneration,
                          delim,
                          columns.length,
                          addColumnValue,
                        ),
                      )
                    }
                  >
                    Write CSV with added column
                  </button>
                </div>
                <p className="bigtools__hint" role="note">
                  The value is appended to every row
                  {hasHeader ? ", including the header row where it becomes the new column name" : ""}.
                </p>
              </div>

              <div className="bigtools__tool">
                <b>Redact / anonymise</b>
                <div className="bigtools__cols">
                  {visibleColumns.map((c, i) => (
                    <label key={i} className="bigtools__check">
                      <input type="checkbox" checked={redact.has(i)} onChange={() => toggle(redact, i, setRedact)} /> {c || `col ${i + 1}`}
                    </label>
                  ))}
                </div>
                <div className="bigtools__row">
                  <select aria-label="Redaction mode" value={redactMode} onChange={(e) => setRedactMode(e.target.value)}>
                    {REDACT_MODES.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </select>
                  {redactMode === "fixed" && (
                    <input
                      aria-label="Fixed redaction replacement"
                      maxLength={BIG_TOOLS_VALUE_INPUT_LIMIT}
                      value={redactRepl}
                      onChange={(e) =>
                        setRedactRepl(
                          boundedBigToolsText(e.target.value, BIG_TOOLS_VALUE_INPUT_LIMIT),
                        )
                      }
                      placeholder="replacement"
                    />
                  )}
                  <button
                    className="btn"
                    aria-label="Run CSV redaction"
                    disabled={unavailable || redact.size === 0}
                    onClick={() =>
                      void run("Redact", () =>
                        BigFile.CsvRedactViaDialog(
                          fileId,
                          sourceGeneration,
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
                  <input
                    aria-label="SQL table name"
                    maxLength={BIG_TOOLS_IDENTIFIER_INPUT_LIMIT}
                    value={sqlTable}
                    onChange={(e) =>
                      setSqlTableName(
                        boundedBigToolsText(
                          e.target.value,
                          BIG_TOOLS_IDENTIFIER_INPUT_LIMIT,
                        ),
                      )
                    }
                    placeholder="table name"
                  />
                  <label className="bigtools__check">
                    <input type="checkbox" checked={includeCreate} onChange={(e) => setIncludeCreate(e.target.checked)} /> CREATE TABLE
                  </label>
                  <button className="btn" aria-label="Convert CSV to SQL file" disabled={unavailable} onClick={() => void run("CSV→SQL", () => BigFile.CsvToSQLViaDialog(fileId, sourceGeneration, delim, sqlTable, hasHeader, includeCreate))}>
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Export</b>
                <BigToolsCsvExportActions
                  disabled={unavailable}
                  numberKeys={numberKeys}
                  onNumberKeysChange={setNumberKeys}
                  onExportJSONL={() =>
                    void run("Export JSONL", () =>
                      BigFile.CsvExportJSONLViaDialog(
                        fileId,
                        sourceGeneration,
                        delim,
                        hasHeader,
                        numberKeys,
                      ),
                    )
                  }
                />
              </div>

              <div className="bigtools__tool">
                <b>Profile columns</b>
                <button
                  className="btn"
                  disabled={unavailable}
                  onClick={() =>
                    void runInspection(
                      "profile",
                      async () => {
                        const value = normalizeBigToolsCsvProfile(
                          await BigFile.CsvProfile(fileId, delim, hasHeader),
                        );
                        if (value.generation !== sourceGeneration) {
                          throw new Error(
                            "CSV profile belongs to a stale source generation. Reopen the tools.",
                          );
                        }
                        return value;
                      },
                      setProfile,
                    )
                  }
                >
                  Profile
                </button>
                {profile && (
                  <>
                    <div className="bigtools__hint" role="status">
                      {profile.recordsScanned.toLocaleString()} rows scanned
                      {profile.truncated ? " (bounded sample)" : ""}
                      {profile.raggedRows > 0
                        ? ` · ${profile.raggedRows.toLocaleString()} ragged rows`
                        : ""}
                    </div>
                    {profile.columns.length === 0 ? (
                      <div className="bigtools__dim">No profile columns returned.</div>
                    ) : (
                      <table className="bigtools__table">
                        <caption className="sr-only">Bounded CSV column profile</caption>
                        <thead>
                          <tr>
                            <th scope="col">column</th>
                            <th scope="col">type</th>
                            <th scope="col">null</th>
                            <th scope="col">distinct</th>
                            <th scope="col">min</th>
                            <th scope="col">max</th>
                          </tr>
                        </thead>
                        <tbody>
                          {profile.columns.slice(0, BIG_TOOLS_COLUMN_DOM_LIMIT).map((c, i) => (
                            <tr key={i}>
                              <td>{c.name}</td>
                              <td>{c.sqlType}</td>
                              <td>{c.null.toLocaleString()}</td>
                              <td>
                                {c.distinct.toLocaleString()}
                                {c.distinctCapped ? "+" : ""}
                              </td>
                              <td>{c.min}</td>
                              <td>{c.max}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )}
                    {profile.columns.length > BIG_TOOLS_COLUMN_DOM_LIMIT && (
                      <div className="bigtools__hint">
                        Profile renders the first {BIG_TOOLS_COLUMN_DOM_LIMIT} columns;{" "}
                        {profile.columns.length - BIG_TOOLS_COLUMN_DOM_LIMIT} are omitted.
                      </div>
                    )}
                  </>
                )}
              </div>
            </>
          ) : visibleFileKind === "sql" ? (
            <>
              <div className="bigtools__tool">
                <b>Analyze dump</b>
                <button
                  type="button"
                  className="btn"
                  aria-label={analysisReady ? "Re-analyze SQL dump" : "Analyze SQL dump"}
                  disabled={unavailable}
                  onClick={() => void analyzeSql()}
                >
                  {analysisLoading
                    ? "Analyzing..."
                    : analysisReady
                      ? "Re-analyze"
                      : "Analyze"}
                </button>
                {!analysisReady && !analysisLoading && (
                  <p className="bigtools__hint" role="note">
                    Analysis is required only for lint, schema/export, split, and fixture tools.
                    Find/replace and reshape can run without it.
                  </p>
                )}
              </div>
              <div className="bigtools__hint" role="note">
                Exports contain only analyzed CREATE, INSERT, and REPLACE regions. Session
                preamble, ALTER/DROP statements, triggers, routines, and other DML may be
                omitted; review the generated copy before importing it.
              </div>
              <div className="bigtools__tool">
                <b>Lint dump</b>
                <button
                  className="btn"
                  disabled={analysisDependentUnavailable}
                  onClick={() =>
                    void runInspection(
                      "lint",
                      async () => normalizeBigToolsSqlLint(await BigFile.SqlLint(fileId)),
                      setLint,
                      true,
                    )
                  }
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
                  <input
                    type="search"
                    maxLength={BIG_TOOLS_IDENTIFIER_INPUT_LIMIT}
                    aria-label="Filter analyzed SQL tables"
                    placeholder="Filter tables"
                    value={tableFilter}
                    disabled={analysisDependentUnavailable}
                    onChange={(e) => {
                      setTableFilter(
                        boundedBigToolsText(
                          e.target.value,
                          BIG_TOOLS_IDENTIFIER_INPUT_LIMIT,
                        ),
                      );
                      setTablePage(0);
                    }}
                  />
                  <select
                    aria-label="Analyzed SQL table"
                    value={selectedTableName}
                    disabled={analysisDependentUnavailable || tableWindow.items.length === 0}
                    onChange={(e) => setTable(e.target.value)}
                  >
                    {tableWindow.items.map((t) => (
                      <option key={t} value={t}>
                        {t}
                      </option>
                    ))}
                  </select>
                  <button
                    className="btn"
                    disabled={
                      analysisDependentUnavailable || !selectedTableCapabilities.extract
                    }
                    onClick={() =>
                      void run(
                        "Extract table",
                        () => BigFile.SqlExtractTableViaDialog(fileId, selectedTableName),
                        true,
                      )
                    }
                  >
                    Whole
                  </button>
                  <button
                    className="btn"
                    title={
                      selectedTable && !selectedTableCapabilities.schema
                        ? "This table has no analyzed CREATE TABLE region."
                        : "Extract this table's analyzed CREATE TABLE region."
                    }
                    disabled={
                      analysisDependentUnavailable || !selectedTableCapabilities.schema
                    }
                    onClick={() =>
                      void run(
                        "Extract schema",
                        () => BigFile.SqlExtractSchemaViaDialog(fileId, selectedTableName),
                        true,
                      )
                    }
                  >
                    Schema
                  </button>
                  <button
                    className="btn"
                    title={
                      selectedTable && !selectedTableCapabilities.data
                        ? "This table has no analyzed INSERT or REPLACE region."
                        : "Extract this table's analyzed INSERT and REPLACE regions."
                    }
                    disabled={
                      analysisDependentUnavailable || !selectedTableCapabilities.data
                    }
                    onClick={() =>
                      void run(
                        "Extract data",
                        () => BigFile.SqlExtractDataViaDialog(fileId, selectedTableName),
                        true,
                      )
                    }
                  >
                    Data
                  </button>
                </div>
                <div className="bigtools__row" aria-label="Analyzed SQL table pages">
                  <button
                    type="button"
                    className="btn"
                    disabled={tableWindow.page === 0}
                    onClick={() => setTablePage((page) => Math.max(0, page - 1))}
                  >
                    Previous tables
                  </button>
                  <span className="bigtools__dim">
                    {tableWindow.matches.length.toLocaleString()} matching · page{" "}
                    {tableWindow.page + 1} of {tableWindow.pageCount}
                  </span>
                  <button
                    type="button"
                    className="btn"
                    disabled={tableWindow.page + 1 >= tableWindow.pageCount}
                    onClick={() =>
                      setTablePage((page) => Math.min(tableWindow.pageCount - 1, page + 1))
                    }
                  >
                    Next tables
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Whole-dump exports</b>
                <div className="bigtools__row">
                  <button
                    type="button"
                    className="btn"
                    disabled={analysisDependentUnavailable}
                    title="Write analyzed CREATE TABLE regions for every discovered table."
                    onClick={() =>
                      void run(
                        "Extract whole schema",
                        () => BigFile.SqlExtractSchemaViaDialog(fileId, ""),
                        true,
                      )
                    }
                  >
                    Write whole schema
                  </button>
                  <button
                    type="button"
                    className="btn"
                    disabled={analysisDependentUnavailable}
                    onClick={() =>
                      void run(
                        "Split by table",
                        () => BigFile.SqlSplitByTableViaDialog(fileId),
                        true,
                      )
                    }
                  >
                    Write one file per table
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Reshape INSERTs</b>
                <div className="bigtools__row">
                  <select aria-label="INSERT reshape mode" value={reshapeMode} onChange={(e) => setReshapeMode(e.target.value)}>
                    <option value="single">single (one row / INSERT)</option>
                    <option value="multi">multi (batched)</option>
                  </select>
                  {reshapeMode === "multi" && (
                    <input
                      aria-label="Rows per reshaped INSERT"
                      type="number"
                      min={1}
                      max={BIG_TOOLS_SQL_ROW_LIMIT}
                      value={batchSize}
                      onChange={(e) =>
                        setBatchSize(
                          boundedBigToolsInteger(
                            e.target.value,
                            1,
                            BIG_TOOLS_SQL_ROW_LIMIT,
                            100,
                          ),
                        )
                      }
                      style={{ width: 80 }}
                    />
                  )}
                  <button className="btn" aria-label="Run SQL INSERT reshape" disabled={unavailable} onClick={() => void run("Reshape", () => BigFile.SqlReshapeInsertsViaDialog(fileId, reshapeMode, batchSize))}>
                    Run
                  </button>
                </div>
                <p className="bigtools__hint" role="note">
                  Reshaping changes statement grouping. Use it only when statement-level triggers,
                  transaction boundaries, rollback behavior, and database side effects are
                  acceptable.
                </p>
              </div>

              <div className="bigtools__tool">
                <b>Sample fixture</b>
                <div className="bigtools__row">
                  <input
                    aria-label="Fixture rows per table"
                    type="number"
                    min={1}
                    max={BIG_TOOLS_SQL_ROW_LIMIT}
                    value={fixtureRows}
                    onChange={(e) =>
                      setFixtureRows(
                        boundedBigToolsInteger(
                          e.target.value,
                          1,
                          BIG_TOOLS_SQL_ROW_LIMIT,
                          100,
                        ),
                      )
                    }
                    style={{ width: 80 }}
                  />{" "}
                  rows / table
                  <button
                    className="btn"
                    aria-label="Create SQL sample fixture"
                    disabled={analysisDependentUnavailable}
                    onClick={() =>
                      void run(
                        "Fixture",
                        () => BigFile.SqlSampleFixtureViaDialog(fileId, fixtureRows),
                        true,
                      )
                    }
                  >
                    Run
                  </button>
                </div>
              </div>

              <div className="bigtools__tool">
                <b>Find / replace → new SQL file</b>
                <div className="bigtools__row">
                  <input aria-label="Find SQL text" value={findStr} onChange={(e) => setFindStr(e.target.value)} placeholder="find text" />
                  <input aria-label="Replace SQL text with" value={replStr} onChange={(e) => setReplStr(e.target.value)} placeholder="replace with" />
                </div>
                <div className="bigtools__row">
                  <label className="bigtools__check">
                    <input type="checkbox" checked={reCase} onChange={(e) => setReCase(e.target.checked)} /> ignore case
                  </label>
                  <label className="bigtools__check">
                    <input type="checkbox" checked={reWhole} onChange={(e) => setReWhole(e.target.checked)} /> whole word
                  </label>
                  <button className="btn" aria-label="Replace SQL text into a new file" disabled={unavailable || Boolean(sqlReplaceReason) || !findStr} onClick={() => void run("Replace", () => BigFile.SqlReplaceViaDialog(fileId, findStr, replStr, false, reCase, reWhole))}>
                    Run
                  </button>
                </div>
                <p className="bigtools__hint" role="note">
                  {sqlReplaceReason ||
                    "Plain replacement updates decoded single-quoted SQL values only and writes a new copy. PHP/WordPress serialized byte lengths are recalculated. Comments, identifiers, and untouched literals stay byte-for-byte unchanged; regex, routines, custom delimiters, ambiguous escapes, and unsupported serialized values stop without publishing output."}
                </p>
              </div>
            </>
          ) : (
            <div className="bigtools__dim" role="status">
              {!targetStateCurrent || loading
                ? "Opening the file and detecting its data type…"
                : "No CSV, TSV, or SQL tools are available for this file."}
            </div>
          )}

          {(isCsv || isSql) && (
            <div className="bigtools__tool">
              <b>Harvest matches (regex → one per line)</b>
              <div className="bigtools__row">
                <input
                  aria-label="Regular expression to extract"
                  value={harvestPat}
                  onChange={(e) => setHarvestPat(e.target.value)}
                  placeholder="regex pattern"
                />
                <label className="bigtools__check">
                  <input type="checkbox" checked={harvestCi} onChange={(e) => setHarvestCi(e.target.checked)} /> ignore case
                </label>
                <button className="btn" aria-label="Extract regular expression matches to a new file" disabled={unavailable || !harvestPat} onClick={() => void run("Harvest", () => BigFile.HarvestMatchesViaDialog(fileId, harvestPat, harvestCi))}>
                  Run
                </button>
              </div>
            </div>
          )}
        </div>

        {targetStateCurrent && err && <div className="bigtools__err" role="alert">{err}</div>}
        {targetStateCurrent && result && <div className="bigtools__ok" role="status">{result}</div>}
        {jobBusy && (
          <div className="bigtools__ok" role="status" aria-live="polite">
            {activeTargetJob
              ? `${activeTargetJob.title} · ${bigFileJobProgressLabel(activeTargetJob)}${activeTargetJob.note ? ` · ${activeTargetJob.note}` : ""}`
              : activeJob
                ? "Another large-file operation is running."
                : analysisLoading
                  ? "Analyzing SQL dump…"
                  : "Starting transform…"}
          </div>
        )}

        <div className="modal__actions">
          {jobBusy && <Loader2 size={14} className="spin" aria-hidden="true" />}
          {activeTargetJob && (
            <button
              type="button"
              className="btn btn--danger"
              aria-label={`Cancel ${activeTargetJob.title}`}
              disabled={canceling}
              onClick={() => void cancelTransform()}
            >
              {canceling ? "Cancelling…" : "Cancel transform"}
            </button>
          )}
          <button type="button" ref={closeRef} className="btn" disabled={closeBusy} onClick={requestClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
