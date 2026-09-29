import { describe, expect, it } from "vitest";
import { highlighterFor, type Seg } from "./lineHighlight";

function sqlSegments(text: string): Seg[] {
  const tokenizer = highlighterFor("sql", "dump.sql");
  if (!tokenizer) throw new Error("SQL tokenizer unavailable");
  return tokenizer(text);
}

function classifiedText(segments: Seg[], cssClass: string): string[] {
  return segments.filter((segment) => segment.c === cssClass).map((segment) => segment.t);
}

describe("SQL line highlighting", () => {
  it("does not mistake MySQL subtraction-like dashes for a comment", () => {
    const segments = sqlSegments("SELECT 4--2, 5--value");
    expect(classifiedText(segments, "hl-comment")).toEqual([]);
    expect(classifiedText(segments, "hl-keyword")).toContain("SELECT");
  });

  it("accepts every MySQL control-space form after two dashes", () => {
    for (const input of ["-- note", "--\tnote", "--\rSELECT", "--\u0001note"]) {
      const comments = classifiedText(sqlSegments(input), "hl-comment");
      expect(comments[0]).toBe(input.split(/\r|\n/, 1)[0]);
    }
  });

  it("ends line comments at CR, LF, and CRLF boundaries", () => {
    for (const separator of ["\r", "\n", "\r\n"]) {
      const segments = sqlSegments(`-- note${separator}SELECT 1`);
      expect(classifiedText(segments, "hl-comment")).toEqual(["-- note"]);
      expect(classifiedText(segments, "hl-keyword")).toContain("SELECT");
    }
  });

  it("keeps comment markers inside quoted values classified as strings", () => {
    const segments = sqlSegments("SELECT '-- note', '# data'");
    expect(classifiedText(segments, "hl-comment")).toEqual([]);
    expect(classifiedText(segments, "hl-string")).toEqual(["'-- note'", "'# data'"]);
  });
});
