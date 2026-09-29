import { useCallback, useEffect, useRef, useState } from "react";
import { Clipboard, Download, Loader2, Search } from "lucide-react";
import { Workspace, Shell, errMessage } from "../lib/services";
import { toCsv, nextSort, type Sort } from "../lib/grid";
import { writeClipboardText } from "../lib/clipboard";
import { useStore } from "../state/store";
import VirtualGrid from "./VirtualGrid";
import { LatestWindowLoader } from "../lib/latestWindowLoader";

const WINDOW = 800; // rows fetched per window
const MARGIN = 200; // rows loaded before the visible start, for scroll slack

// TableView previews large CSV/TSV/XLSX inputs with a sliding UI window: only
// ~one screenful (plus slack) is retained by this component. Browsing requests the visible
// window off disk (and a background TableInfo counts rows + builds a seek index
// so the scrollbar spans the whole file and jumps are fast); a filter or sort is
// computed over the whole file by the backend and served from cache.
export default function TableView({ rel, sourceVersion = 0 }: { rel: string; sourceVersion?: number }) {
  const setStatus = useStore((s) => s.setStatus);
  const [columns, setColumns] = useState<string[]>([]);
  const [sheet, setSheet] = useState("");
  const [detected, setDetected] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [capped, setCapped] = useState(false);

  const [windowStart, setWindowStart] = useState(0);
  const [windowRows, setWindowRows] = useState<string[][]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [totalRows, setTotalRows] = useState(-1); // -1 = unknown (browse, still counting)
  const [loading, setLoading] = useState(false);

  const [filter, setFilter] = useState("");
  const [debFilter, setDebFilter] = useState("");
  const [delimiter, setDelimiter] = useState("");
  const [sort, setSort] = useState<Sort>(null);

  const sortCol = sort ? sort.col : -1;
  const sortDir = sort ? sort.dir : 1;
  const isDelimited = /\.(csv|tsv)$/i.test(rel);
  const filteringOrSorting = debFilter.trim() !== "" || sortCol >= 0;

  const fetchTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const windowLoader = useRef<LatestWindowLoader<Awaited<ReturnType<typeof Workspace.QueryTable>>> | null>(null);

  const query = useCallback(
    (offset: number) =>
      Workspace.QueryTable(rel, { offset, limit: WINDOW, filter: debFilter, sortCol, sortDir, delimiter }),
    // sourceVersion is intentionally part of the request identity. A watcher
    // event must refetch the same path/settings rather than reuse stale rows.
    [rel, debFilter, sortCol, sortDir, delimiter, sourceVersion],
  );

  // Debounce the filter so a full-file scan doesn't run on every keystroke.
  useEffect(() => {
    const id = setTimeout(() => setDebFilter(filter), 350);
    return () => clearTimeout(id);
  }, [filter]);

  // Reset + load the first window (and, for browse, the background row count)
  // whenever the file, filter, sort, or delimiter changes.
  useEffect(() => {
    let alive = true;
    let initial = true;
    clearTimeout(fetchTimer.current);
    setError("");
    setMessage("");
    setColumns([]);
    setWindowRows([]);
    setWindowStart(0);
    setTotalRows(-1);
    setHasMore(false);
    const loader = new LatestWindowLoader({
      query,
      loaded: (p, offset) => {
        initial = false;
        setError("");
        setColumns(p.columns);
        setSheet(p.sheet);
        setDetected(p.delimiter);
        setCapped(p.capped);
        setMessage(p.message);
        setWindowRows(p.rows);
        setWindowStart(offset);
        setHasMore(p.hasMore);
        if (p.totalRows >= 0) setTotalRows(p.totalRows); // filter/sort: exact total
      },
      failed: (e) => {
        if (initial) setError(errMessage(e));
        else setStatus(errMessage(e), "error");
      },
      loading: setLoading,
    });
    windowLoader.current = loader;
    loader.request(0);
    if (!filteringOrSorting) {
      Workspace.TableInfo(rel, delimiter)
        .then((info) => {
          if (alive) setTotalRows(info.rows);
        })
        .catch(() => {});
    }
    return () => {
      alive = false;
      loader.dispose();
      windowLoader.current = null;
      clearTimeout(fetchTimer.current);
    };
  }, [query, filteringOrSorting, rel, delimiter, setStatus]);

  const onVisibleRange = useCallback(
    (start: number, end: number) => {
      clearTimeout(fetchTimer.current);
      if (start >= windowStart && end <= windowStart + windowRows.length) return; // covered
      const offset = Math.max(0, start - MARGIN);
      if (offset === windowStart && windowRows.length > 0) return;
      // Capture this query's loader. A timer that races effect cleanup cannot
      // issue the previous filter's offset against a replacement query.
      const loader = windowLoader.current;
      fetchTimer.current = setTimeout(() => loader?.request(offset), 90);
    },
    [windowStart, windowRows.length],
  );

  const baseName = rel.split("/").pop() ?? "data";
  const copyCsv = async () => {
    try {
      await writeClipboardText(toCsv(columns, windowRows));
      setStatus(`Copied the visible window (${windowRows.length} rows) as CSV.`, "success");
    } catch (error) {
      setStatus(errMessage(error), "error");
    }
  };
  const exportCsv = async () => {
    try {
      const path = await Shell.SaveTextFile(baseName.replace(/\.[^.]+$/, "") + ".csv", toCsv(columns, windowRows));
      if (path) setStatus(`Exported the visible window (${windowRows.length} rows) to ${path}`, "success");
    } catch (e) {
      setStatus(errMessage(e), "error");
    }
  };

  if (error) return <div className="dbq__error">{error}</div>;

  // Virtualize over the exact total when known; until the background count lands,
  // use a provisional total that grows with the frontier so forward scroll works.
  const effectiveTotal = totalRows >= 0 ? totalRows : windowStart + windowRows.length + (hasMore ? WINDOW : 0);
  const countLabel =
    totalRows >= 0
      ? `${totalRows.toLocaleString()} row${totalRows === 1 ? "" : "s"}${capped ? " (capped — narrow the filter)" : ""}`
      : `counting… (${(windowStart + windowRows.length).toLocaleString()}+ loaded)`;

  return (
    <div className="dbq">
      <div className="dbq__bar">
        <span className="dbq__filterwrap">
          <Search size={13} className="dbq__filtericon" aria-hidden focusable={false} />
          <input
            className="dbq__filter"
            value={filter}
            spellCheck={false}
            placeholder="Filter rows (whole file)…"
            onChange={(e) => setFilter(e.target.value)}
          />
        </span>
        {isDelimited && (
          <select className="dbq__limit" value={delimiter} onChange={(e) => setDelimiter(e.target.value)} title="Field separator">
            <option value="">{detected ? `Auto (${detected})` : "Auto"}</option>
            <option value="comma">Comma ,</option>
            <option value="semicolon">Semicolon ;</option>
            <option value="tab">Tab ⇥</option>
            <option value="pipe">Pipe |</option>
          </select>
        )}
        {loading && <Loader2 size={13} className="spin" />}
        <span className="dbq__meta">
          {sheet ? `${sheet} · ` : ""}
          {countLabel}
          {columns.length > 0 ? ` × ${columns.length} col` : ""}
        </span>
        {columns.length > 0 && (
          <>
            <button className="btn" onClick={() => void copyCsv()} title="Copy the visible window as CSV">
              <Clipboard size={13} /> Copy
            </button>
            <button className="btn" onClick={() => void exportCsv()} title="Export the visible window to a CSV file">
              <Download size={13} /> Export
            </button>
          </>
        )}
      </div>
      <div className="dbq__results">
        {message && windowRows.length === 0 ? (
          <div className="dbq__hint">{message}</div>
        ) : columns.length === 0 ? (
          <div className="dbq__hint">Loading…</div>
        ) : (
          <VirtualGrid
            columns={columns}
            rows={windowRows}
            windowStart={windowStart}
            totalRows={effectiveTotal}
            sort={sort}
            onSort={(i) => setSort((s) => nextSort(s, i))}
            onVisibleRange={onVisibleRange}
            rainbow={isDelimited}
          />
        )}
      </div>
    </div>
  );
}
