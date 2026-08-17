import { describe, expect, it } from "vitest";
import {
  bigFileLineTarget,
  exactSearchLocation,
  normalizeBigFileHexWindow,
  normalizeBigFileLineResolution,
  normalizeBigFileMatchWindow,
  normalizeBigFileState,
  normalizeBigFileTextWindow,
  parseBigFileLocation,
  requireAdjacentPreviousTextWindow,
} from "./bigFileNavigation";

describe("large-file navigation inputs", () => {
  it("accepts exact line, hexadecimal, and percentage locations", () => {
    expect(parseBigFileLocation("42", 1_000)).toEqual({ kind: "line", line: 42 });
    expect(parseBigFileLocation("0x2a", 1_000)).toEqual({ kind: "byte", offset: 42 });
    expect(parseBigFileLocation("12.5%", 1_000)).toEqual({ kind: "byte", offset: 125 });
  });

  it("rejects partial, out-of-range, and inexact locations", () => {
    expect(parseBigFileLocation("12oops", 1_000).kind).toBe("error");
    expect(parseBigFileLocation("101%", 1_000).kind).toBe("error");
    expect(parseBigFileLocation("0x20000000000001", 1_000).kind).toBe("error");
    expect(parseBigFileLocation("50%", Number.MAX_SAFE_INTEGER + 1).kind).toBe("error");
  });

  it("rejects search anchors that cannot round-trip exactly through JavaScript", () => {
    expect(exactSearchLocation(10, 3, 2)).toEqual({
      ok: true,
      location: { offset: 10, length: 3, line: 2 },
    });
    expect(exactSearchLocation(Number.MAX_SAFE_INTEGER + 1, 1, 2).ok).toBe(false);
    expect(exactSearchLocation(Number.MAX_SAFE_INTEGER, 1, 2).ok).toBe(false);
    expect(exactSearchLocation(10, 1, Number.NaN).ok).toBe(false);
  });

  it("accepts bounded text windows, including a single blank line", () => {
    expect(
      normalizeBigFileTextWindow(
        {
          fileId: "file-a",
          startByte: 0,
          nextByte: 1,
          text: "",
          lineOffsets: [0],
          lineNumbers: [1],
          atBof: true,
          atEof: true,
          approx: false,
        },
        "file-a",
      ),
    ).toMatchObject({ fileId: "file-a", lineOffsets: [0], lineNumbers: [1] });
  });

  it("rejects stale, mismatched, oversized, and inexact text windows", () => {
    const valid = {
      fileId: "file-a",
      startByte: 0,
      nextByte: 4,
      text: "a\nb",
      lineOffsets: [0, 2],
      lineNumbers: [1, 2],
      atBof: true,
      atEof: true,
      approx: false,
    };

    expect(() => normalizeBigFileTextWindow(valid, "file-b")).toThrow(/different file/i);
    expect(() =>
      normalizeBigFileTextWindow({ ...valid, lineNumbers: [1] }, "file-a"),
    ).toThrow(/counts do not match/i);
    expect(() =>
      normalizeBigFileTextWindow({ ...valid, text: "a" }, "file-a"),
    ).toThrow(/text\/coordinate/i);
    expect(() => normalizeBigFileTextWindow(valid, "file-a", 1)).toThrow(/too many rows/i);
    expect(() =>
      normalizeBigFileTextWindow(
        { ...valid, nextByte: Number.MAX_SAFE_INTEGER + 1 },
        "file-a",
      ),
    ).toThrow(/inexact/i);
    expect(() =>
      normalizeBigFileTextWindow({ ...valid, text: "x".repeat(8 * 1024 * 1024 + 1) }, "file-a"),
    ).toThrow(/too much decoded text/i);
  });

  it("rejects a previous text window that leaves a viewport gap", () => {
    const previous = normalizeBigFileTextWindow(
      {
        fileId: "file-a",
        startByte: 10,
        nextByte: 20,
        text: "row",
        lineOffsets: [10],
        lineNumbers: [2],
        atBof: false,
        atEof: false,
        approx: false,
      },
      "file-a",
    );
    expect(requireAdjacentPreviousTextWindow(previous, 20)).toBe(previous);
    expect(() => requireAdjacentPreviousTextWindow(previous, 21)).toThrow(/not adjacent/i);
    expect(() => requireAdjacentPreviousTextWindow(previous, Number.MAX_SAFE_INTEGER + 1)).toThrow(
      /exact byte-navigation/i,
    );
  });

  it("validates follow-tail identity state and exact match coordinates", () => {
    expect(
      normalizeBigFileState({
        size: 42,
        modTimeUnixMillis: 1_700_000_000_000,
        sameOpenedFile: true,
        changedFromOpen: false,
      }),
    ).toMatchObject({ size: 42, sameOpenedFile: true });
    expect(() =>
      normalizeBigFileState({
        size: Number.MAX_SAFE_INTEGER + 1,
        modTimeUnixMillis: 0,
        sameOpenedFile: true,
        changedFromOpen: false,
      }),
    ).toThrow(/inexact file size/i);

    const window = {
      fileId: "file-a",
      startByte: 0,
      nextByte: 8,
      text: "a😀hit",
      lineOffsets: [0],
      lineNumbers: [1],
      atBof: true,
      atEof: true,
      approx: false,
    };
    expect(
      normalizeBigFileMatchWindow({ window, found: true, from: 3, to: 6 }, "file-a"),
    ).toMatchObject({ found: true, from: 3, to: 6 });
    expect(() =>
      normalizeBigFileMatchWindow({ window, found: true, from: 3, to: 20 }, "file-a"),
    ).toThrow(/outside the decoded window/i);
    expect(() =>
      normalizeBigFileMatchWindow({ window, found: false, from: 1, to: 1 }, "file-a"),
    ).toThrow(/unavailable match/i);
  });

  it("normalizes nil hex slices and rejects malformed hex rows", () => {
    expect(
      normalizeBigFileHexWindow({
        fileId: "file-a",
        startByte: 0,
        nextByte: 0,
        lines: null,
        atBof: true,
        atEof: true,
      }, "file-a"),
    ).toMatchObject({ fileId: "file-a", lines: [] });

    expect(() =>
      normalizeBigFileHexWindow({
        fileId: "file-a",
        startByte: 0,
        nextByte: 16,
        lines: [{ offset: Number.NaN, hex: "00", ascii: "." }],
        atBof: true,
        atEof: false,
      }, "file-a"),
    ).toThrow(/inexact/i);

    expect(() =>
      normalizeBigFileHexWindow(
        {
          fileId: "file-a",
          startByte: 0,
          nextByte: 0,
          lines: [],
          atBof: true,
          atEof: true,
        },
        "file-b",
      ),
    ).toThrow(/different file/i);
  });

  it("preserves offset zero as an exact or fallback line position", () => {
    expect(
      bigFileLineTarget(
        {
          offset: 0,
          resolvedLine: 1,
          exact: true,
          found: true,
          indexComplete: false,
          limited: false,
        },
        1,
      ),
    ).toEqual({ kind: "position", offset: 0, exact: true, message: "" });

    expect(
      bigFileLineTarget(
        {
          offset: 0,
          resolvedLine: 1,
          exact: false,
          found: true,
          indexComplete: true,
          limited: true,
        },
        2,
      ),
    ).toEqual({
      kind: "position",
      offset: 0,
      exact: false,
      message: "Exact line lookup reached its bounded scan limit; using nearest known line 1 instead.",
    });
  });

  it("distinguishes pending, missing, and limited-without-position results", () => {
    expect(
      bigFileLineTarget(
        {
          offset: 0,
          resolvedLine: 0,
          exact: false,
          found: false,
          indexComplete: false,
          limited: false,
        },
        20,
      ),
    ).toEqual({ kind: "unavailable", message: "Line index is not ready yet." });

    expect(
      bigFileLineTarget(
        {
          offset: 0,
          resolvedLine: 0,
          exact: false,
          found: false,
          indexComplete: true,
          limited: false,
        },
        20,
      ),
    ).toEqual({ kind: "unavailable", message: "Line does not exist." });

    expect(
      bigFileLineTarget(
        {
          offset: 0,
          resolvedLine: 0,
          exact: false,
          found: false,
          indexComplete: true,
          limited: true,
        },
        20,
      ).message,
    ).toMatch(/bounded scan limit/i);
  });

  it("rejects malformed and inexact line-resolution payloads", () => {
    const exact = {
      offset: 4,
      resolvedLine: 2,
      exact: true,
      found: true,
      indexComplete: true,
      limited: false,
    };
    expect(() =>
      normalizeBigFileLineResolution({ ...exact, offset: Number.MAX_SAFE_INTEGER + 1 }),
    ).toThrow(/inexact/i);
    expect(() => normalizeBigFileLineResolution({ ...exact, found: false })).toThrow(
      /not found/i,
    );
    expect(() => normalizeBigFileLineResolution({ ...exact, limited: true })).toThrow(
      /both exact and scan-limited/i,
    );
    expect(() => bigFileLineTarget({ ...exact, resolvedLine: 3 }, 2)).toThrow(
      /after the requested line/i,
    );
  });
});
