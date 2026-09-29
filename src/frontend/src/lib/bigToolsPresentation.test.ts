import { describe, expect, it } from "vitest";
import {
  BIG_TOOLS_TABLE_PAGE_SIZE,
  BIG_TOOLS_VALUE_INPUT_LIMIT,
  BigToolsPayloadError,
  bigToolsSqlTableCapabilities,
  boundedBigToolsInteger,
  boundedBigToolsText,
  isValidBigToolsCsvDelimiter,
  normalizeBigToolsCsvProfile,
  normalizeBigToolsCsvSchema,
  normalizeBigToolsSqlLint,
  normalizeBigToolsSqlTables,
  pageMatchingStrings,
} from "./bigToolsPresentation";

describe("Big Tools bridge presentation", () => {
  it("normalizes nullable Go slices before React renders them", () => {
    expect(normalizeBigToolsCsvSchema({ generation: 1, columns: null, warnings: null })).toEqual({
      generation: 1,
      columns: [],
      warnings: [],
    });
    expect(
      normalizeBigToolsCsvProfile({
        generation: 1,
        columns: null,
        recordsScanned: 0,
        raggedRows: 0,
        truncated: false,
      }).columns,
    ).toEqual([]);
    expect(normalizeBigToolsSqlLint({ findings: null })).toEqual({ findings: [] });
    expect(normalizeBigToolsSqlTables({ tables: null })).toEqual([]);
  });

  it("rejects malformed or oversized bridge collections", () => {
    expect(() => normalizeBigToolsCsvSchema({ generation: 1, columns: "many", warnings: [] })).toThrow(
      BigToolsPayloadError,
    );
    expect(() =>
      normalizeBigToolsSqlTables({
        tables: Array.from({ length: 10_001 }, (_, index) => ({ name: `t${index}` })),
      }),
    ).toThrow(/at most 10000/);
  });

  it("preserves SQL table offsets and derives extraction capabilities", () => {
    const tables = normalizeBigToolsSqlTables({
      tables: [
        { name: "ddl_only", createOffset: 0, insertOffset: -1, bytes: 10 },
        { name: "data_only", createOffset: -1, insertOffset: 20, bytes: 30 },
        { name: "complete", createOffset: 0, insertOffset: 20, bytes: 40 },
      ],
    });

    expect(tables).toEqual([
      { name: "ddl_only", createOffset: 0, insertOffset: -1, bytes: 10 },
      { name: "data_only", createOffset: -1, insertOffset: 20, bytes: 30 },
      { name: "complete", createOffset: 0, insertOffset: 20, bytes: 40 },
    ]);
    expect(bigToolsSqlTableCapabilities(tables[0])).toEqual({
      extract: true,
      schema: true,
      data: false,
    });
    expect(bigToolsSqlTableCapabilities(tables[1])).toEqual({
      extract: true,
      schema: false,
      data: true,
    });
    expect(bigToolsSqlTableCapabilities(tables[2])).toEqual({
      extract: true,
      schema: true,
      data: true,
    });
    expect(bigToolsSqlTableCapabilities(null)).toEqual({
      extract: false,
      schema: false,
      data: false,
    });
  });

  it("rejects missing or invalid SQL table metadata", () => {
    expect(() =>
      normalizeBigToolsSqlTables({
        tables: [{ name: "missing", createOffset: 0, insertOffset: 1 }],
      }),
    ).toThrow(BigToolsPayloadError);
    expect(() =>
      normalizeBigToolsSqlTables({
        tables: [{ name: "unsafe", createOffset: -2, insertOffset: 1, bytes: 10 }],
      }),
    ).toThrow(BigToolsPayloadError);
    expect(() =>
      normalizeBigToolsSqlTables({
        tables: [
          {
            name: "fraction",
            createOffset: 0,
            insertOffset: 1.5,
            bytes: Number.MAX_SAFE_INTEGER,
          },
        ],
      }),
    ).toThrow(BigToolsPayloadError);
  });

  it("requires exact positive CSV source generations", () => {
    for (const generation of [undefined, null, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      expect(() =>
        normalizeBigToolsCsvSchema({ generation, columns: [], warnings: [] }),
      ).toThrow(BigToolsPayloadError);
    }
    expect(
      normalizeBigToolsCsvSchema({ generation: Number.MAX_SAFE_INTEGER, columns: [], warnings: [] })
        .generation,
    ).toBe(Number.MAX_SAFE_INTEGER);
  });

  it("pages filtered SQL tables without materializing every option", () => {
    const tables = Array.from({ length: 450 }, (_, index) => `table_${index}`);
    const first = pageMatchingStrings(tables, "", 0);
    const last = pageMatchingStrings(tables, "", 99);
    const filtered = pageMatchingStrings(tables, "table_44", 0);

    expect(first.items).toHaveLength(BIG_TOOLS_TABLE_PAGE_SIZE);
    expect(last.page).toBe(2);
    expect(last.items).toHaveLength(50);
    expect(filtered.matches).toEqual([
      "table_44",
      "table_440",
      "table_441",
      "table_442",
      "table_443",
      "table_444",
      "table_445",
      "table_446",
      "table_447",
      "table_448",
      "table_449",
    ]);
  });

  it("bounds controlled text and numeric configuration state", () => {
    expect(boundedBigToolsText(`a\ud83d\ude00`, 2)).toBe("a");
    expect(
      boundedBigToolsText("x".repeat(BIG_TOOLS_VALUE_INPUT_LIMIT + 1), BIG_TOOLS_VALUE_INPUT_LIMIT),
    ).toHaveLength(BIG_TOOLS_VALUE_INPUT_LIMIT);
    expect(boundedBigToolsInteger("99999", 1, 10_000, 100)).toBe(10_000);
    expect(boundedBigToolsInteger("not-a-number", 1, 10_000, 100)).toBe(100);
  });

  it("accepts exactly one representable CSV delimiter rune", () => {
    for (const delimiter of [",", "\t", " ", "§", "😀"]) {
      expect(isValidBigToolsCsvDelimiter(delimiter)).toBe(true);
    }
    for (const delimiter of ["", "||", "\0", "\r", "\n", '"', "\uFFFD"]) {
      expect(isValidBigToolsCsvDelimiter(delimiter)).toBe(false);
    }
  });
});
