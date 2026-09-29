// Shared helpers for the result/table grids: CSV export and numeric-aware sort.

const csvField = (v: string) => {
  // Neutralise spreadsheet formula injection: a cell starting with = + - @ (or
  // a tab/CR) can execute as a formula when the CSV is opened in Excel/Sheets,
  // so prefix it with an apostrophe to force text.
  const cell = /^[=+\-@\t\r]/.test(v) ? `'${v}` : v;
  return /[",\n\r]/.test(cell) ? `"${cell.replace(/"/g, '""')}"` : cell;
};

export function toCsv(columns: string[], rows: string[][]): string {
  const lines = [columns.map(csvField).join(",")];
  for (const r of rows) lines.push(r.map(csvField).join(","));
  return lines.join("\n");
}

export type Sort = { col: number; dir: 1 | -1 } | null;

function asNumber(s: string): number | null {
  const t = s.trim();
  if (t === "") return null;
  // Only treat plain decimals as numbers — no hex (0x…), no exponent (1e3), no
  // leading zeros (e.g. "007"), which usually denote identifiers/codes that
  // should sort lexicographically rather than be coerced.
  if (!/^-?(0|[1-9]\d*)(\.\d+)?$/.test(t)) return null;
  const n = Number(t);
  return Number.isFinite(n) ? n : null;
}

// sortIndices returns the row order produced by a sort, so a caller can reorder
// rows AND any parallel array (e.g. a NULL mask) consistently with one permutation.
export function sortIndices(rows: string[][], sort: Sort): number[] {
  const order = rows.map((_, i) => i);
  if (!sort) return order;
  const { col, dir } = sort;
  return order.sort((ia, ib) => {
    const av = rows[ia][col] ?? "";
    const bv = rows[ib][col] ?? "";
    const an = asNumber(av);
    const bn = asNumber(bv);
    const cmp = an !== null && bn !== null ? an - bn : av.localeCompare(bv);
    return cmp * dir;
  });
}

export function sortRows(rows: string[][], sort: Sort): string[][] {
  if (!sort) return rows;
  return sortIndices(rows, sort).map((i) => rows[i]);
}

// Cycle a column header: none → asc → desc → none.
export function nextSort(prev: Sort, col: number): Sort {
  if (!prev || prev.col !== col) return { col, dir: 1 };
  if (prev.dir === 1) return { col, dir: -1 };
  return null;
}

export function sortIndicator(sort: Sort, col: number): string {
  if (!sort || sort.col !== col) return "";
  return sort.dir === 1 ? " ▲" : " ▼";
}
