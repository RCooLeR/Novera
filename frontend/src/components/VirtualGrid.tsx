import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { sortIndicator, type Sort } from "../lib/grid";

// A windowed, sortable result grid: only the rows in (and near) the viewport are
// in the DOM, with spacer rows preserving scroll height — so a 5000-row result
// stays smooth. Column widths are computed once per dataset (content-aware) and
// the layout is fixed, so columns don't jump as rows scroll. The real row
// height is measured after render to keep the window math drift-free.
const OVERSCAN = 10;
const CH = 7.6; // approx px per monospace char at our font size
const CELL_PAD = 22; // horizontal padding + border, px
const MIN_COL = 5; // chars
const MAX_COL = 60; // chars
const WIDTH_SAMPLE = 1000; // rows scanned for width (kept stable thereafter)

export default function VirtualGrid({
  columns,
  rows,
  nulls,
  sort,
  onSort,
  onNearEnd,
  loadingMore,
  totalRows,
  windowStart,
  onVisibleRange,
}: {
  columns: string[];
  rows: string[][];
  // Optional mask parallel to rows: true marks a real SQL NULL, so NULL styling
  // is keyed off identity rather than the literal text "NULL" (which a real value
  // could also be). Omitted for non-DB grids (CSV/XLSX), which have no NULLs.
  nulls?: boolean[][];
  sort: Sort;
  onSort: (col: number) => void;
  // Fired when the rendered window reaches the end of the loaded rows, so the
  // parent can fetch the next page (lazy loading for large files).
  onNearEnd?: () => void;
  loadingMore?: boolean;
  // --- windowed mode (large files) ---
  // When totalRows is provided, the grid virtualizes over the FULL dataset:
  // `rows` is only the loaded window starting at absolute index `windowStart`,
  // unloaded rows render as placeholders, and onVisibleRange reports the absolute
  // [start,end) so the parent can fetch the covering window.
  totalRows?: number;
  windowStart?: number;
  onVisibleRange?: (start: number, end: number) => void;
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const rowRef = useRef<HTMLTableRowElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportH, setViewportH] = useState(480);
  const [rowH, setRowH] = useState(25);

  const colWidths = useMemo(() => {
    const chars = columns.map((c) => c.length);
    const n = Math.min(rows.length, WIDTH_SAMPLE);
    for (let r = 0; r < n; r++) {
      const row = rows[r];
      for (let i = 0; i < chars.length; i++) {
        const len = row[i] ? row[i].length : 0;
        if (len > chars[i]) chars[i] = len;
      }
    }
    return chars.map((c) => Math.round(Math.min(Math.max(c, MIN_COL), MAX_COL) * CH + CELL_PAD));
  }, [columns, rows]);

  const windowed = totalRows != null;
  const ws = windowStart ?? 0;
  const total = windowed ? totalRows : rows.length;
  const rownumW = Math.max(40, String(total).length * 9 + 16);
  const totalW = rownumW + colWidths.reduce((a, b) => a + b, 0);

  // Self-correct the assumed row height / viewport from the real DOM so the
  // spacer math can't drift over thousands of rows.
  useLayoutEffect(() => {
    if (rowRef.current) {
      const h = rowRef.current.getBoundingClientRect().height;
      if (h > 0 && Math.abs(h - rowH) > 0.5) setRowH(h);
    }
    if (scrollRef.current) {
      const vh = scrollRef.current.clientHeight;
      if (vh > 0 && Math.abs(vh - viewportH) > 1) setViewportH(vh);
    }
  });

  const start = Math.max(0, Math.floor(scrollTop / rowH) - OVERSCAN);
  const end = Math.min(total, Math.ceil((scrollTop + viewportH) / rowH) + OVERSCAN);
  const topPad = start * rowH;
  const botPad = Math.max(0, (total - end) * rowH);
  const colSpan = columns.length + 1;

  // Append mode: ask the parent for more when the viewport reaches loaded rows.
  useEffect(() => {
    if (onNearEnd && !windowed && total > 0 && end >= total) onNearEnd();
  }, [onNearEnd, windowed, end, total]);

  // Windowed mode: report the absolute visible range so the parent can load the
  // covering window (the parent debounces and guards against redundant fetches).
  useEffect(() => {
    if (onVisibleRange && windowed) onVisibleRange(start, end);
  }, [onVisibleRange, windowed, start, end]);

  return (
    <div className="vgrid" ref={scrollRef} onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}>
      <table className="dbq__table vgrid__table" style={{ width: totalW }}>
        <colgroup>
          <col style={{ width: rownumW }} />
          {colWidths.map((w, i) => (
            <col key={i} style={{ width: w }} />
          ))}
        </colgroup>
        <thead>
          <tr>
            <th className="dbq__rownum">#</th>
            {columns.map((c, i) => (
              <th key={i} className="dbq__sortable" onClick={() => onSort(i)}>
                {c}
                {sortIndicator(sort, i)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {topPad > 0 && (
            <tr aria-hidden style={{ height: topPad }}>
              <td colSpan={colSpan} />
            </tr>
          )}
          {Array.from({ length: Math.max(0, end - start) }, (_, vi) => {
            const ri = start + vi;
            const li = ri - ws; // index within the loaded window
            const row = li >= 0 && li < rows.length ? rows[li] : null;
            return (
              <tr key={ri} ref={vi === 0 ? rowRef : undefined} style={{ height: rowH }}>
                <td className="dbq__rownum">{ri + 1}</td>
                {row
                  ? row.map((cell, ci) => (
                      <td key={ci} className={nulls?.[li]?.[ci] ? "dbq__null" : ""}>
                        {cell}
                      </td>
                    ))
                  : columns.map((_, ci) => (
                      <td key={ci} className="vgrid__pending">
                        ·
                      </td>
                    ))}
              </tr>
            );
          })}
          {botPad > 0 && (
            <tr aria-hidden style={{ height: botPad }}>
              <td colSpan={colSpan} />
            </tr>
          )}
          {loadingMore && (
            <tr>
              <td className="dbq__empty" colSpan={colSpan}>
                Loading more…
              </td>
            </tr>
          )}
          {total === 0 && !loadingMore && (
            <tr>
              <td className="dbq__empty" colSpan={colSpan}>
                No rows
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
