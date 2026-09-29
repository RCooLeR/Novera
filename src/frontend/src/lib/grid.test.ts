import { describe, expect, it } from "vitest";
import { toCsv, sortIndices } from "./grid";
import { isTabular, languageForPath } from "./lang";

describe("grid", () => {
  it("neutralizes CSV formula injection (GRID-1)", () => {
    const csv = toCsv(["a"], [["=1+1"], ["safe"]]);
    expect(csv).toContain("'=1+1");
    expect(csv).toContain("safe");
  });

  it("does not coerce id-like strings to numbers (GRID-2)", () => {
    const rows = [["007"], ["1"], ["010"]];
    const order = sortIndices(rows, { col: 0, dir: 1 });
    // Leading-zero codes sort lexically, not numerically.
    expect(order.map((i) => rows[i][0])).toEqual(["007", "010", "1"]);
  });

  it("sortIndices orders real numbers numerically and is a permutation", () => {
    const rows = [["10"], ["2"], ["1"]];
    const order = sortIndices(rows, { col: 0, dir: 1 });
    expect(order.map((i) => rows[i][0])).toEqual(["1", "2", "10"]);
    expect([...order].sort()).toEqual([0, 1, 2]);
  });
});

describe("lang", () => {
  it("detects tabular by basename, not a dotted directory (COR-15)", () => {
    expect(isTabular("a.b.dir/file.csv")).toBe(true);
    expect(isTabular("weird.csv/notdata.txt")).toBe(false);
  });

  it("maps language by extension", () => {
    expect(languageForPath("src/x.ts")).toBe("typescript");
    expect(languageForPath("go.mod")).toBe("go");
  });
});
