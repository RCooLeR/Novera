import { useCallback, useEffect, useRef, useState } from "react";
import { Clipboard, Download, Loader2, Search } from "lucide-react";
import { Workspace, Shell, errMessage } from "../lib/services";
import { toCsv, nextSort, type Sort } from "../lib/grid";
import { useStore } from "../state/store";
import VirtualGrid from "./VirtualGrid";

const WINDOW = 800; // rows fetched per window
const MARGIN = 200; // rows loaded before the visible start, for scroll slack

// TableView previews CSV/TSV/XLSX of any size with a single sliding window: only
// ~one screenful (plus slack) is ever in memory. Browsing streams the visible
// window off disk (and a background TableInfo counts rows + builds a seek index
// so the scrollbar spans the whole file and jumps are fast); a filter or sort is
// computed over the whole file by the backend and served from cache.
export default function TableView({ rel }: { rel: string }) {
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
  const inflight = useRef(false);
  const wantOffset = useRef<number | null>(null);

  const query = useCallback(
    (offset: number) =>
      Workspace.QueryTable(rel, { offset, limit: WINDOW, filter: debFilter, sortCol, sortDir, delimiter }),
    [rel, debFilter, sortCol, sortDir, delimiter],
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
    setError("");
    setMessage("");
    setWindowRows([]);
    setWindowStart(0);
    setTotalRows(-1);
    setLoading(true);
    inflight.current = true;
    wantOffset.current = null;
    query(0)
      .then((p) => {
        if (!alive) return;
        setColumns(p.columns);
        setSheet(p.sheet);
        setDetected(p.delimiter);
        setCapped(p.capped);
        setMessage(p.message);
        setWindowRows(p.rows);
        setWindowStart(0);
        setHasMore(p.hasMore);
        if (p.totalRows >= 0) setTotalRows(p.totalRows); // filter/sort: exact total
      })
      .catch((e) => alive && setError(errMessage(e)))
      .finally(() => {
        if (alive) setLoading(false);
        inflight.current = false;
      });
    if (!filteringOrSorting) {
      Workspace.TableInfo(rel, delimiter)
        .then((info) => alive && setTotalRows(info.rows))
        .catch(() => {});
    }
    return () => {
      alive = false;
    };
  }, [query, filteringOrSorting, rel, delimiter]);

  const loadWindow = useCallback(
    (offset: number) => {
      if (inflight.current) {
        wantOffset.current = offset; // coalesce: remember the latest request
        return;
      }
      inflight.current = true;
      setLoading(true);
      query(offset)
        .then((p) => {
          setColumns(p.columns);
          setWindowStart(offset);
          setWindowRows(p.rows);
          setHasMore(p.hasMore);
          if (p.totalRows >= 0) setTotalRows(p.totalRows);
        })
        .catch((e) => setStatus(errMessage(e), "error"))
        .finally(() => {
          inflight.current = false;
          setLoading(false);
          const next = wantOffset.current;
          wantOffset.current = null;
          if (next != null && next !== offset) loadWindow(next);
        });
    },
    [query, setStatus],
  );

  const onVisibleRange = useCallback(
    (start: number, end: number) => {
      if (start >= windowStart && end <= windowStart + windowRows.length) return; // covered
      const offset = Math.max(0, start - MARGIN);
      if (offset === windowStart && windowRows.length > 0) return;
      clearTimeout(fetchTimer.current);
      fetchTimer.current = setTimeout(() => loadWindow(offset), 90);
    },
    [windowStart, windowRows.length, loadWindow],
  );
  useEffect(() => () => clearTimeout(fetchTimer.current), []);

  const baseName = rel.split("/").pop() ?? "data";
  const copyCsv = () => void navigator.clipboard?.writeText(toCsv(columns, windowRows));
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
            <button className="btn" onClick={copyCsv} title="Copy the visible window as CSV">
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
