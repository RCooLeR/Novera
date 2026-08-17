import { describe, expect, it } from "vitest";
import { sqlReplaceUnavailableReason } from "./sqlReplaceCapability";

describe("sqlReplaceUnavailableReason", () => {
  it("allows non-binary UTF-8 SQL", () => {
    expect(sqlReplaceUnavailableReason({ binary: false, encoding: "UTF-8", detected: "SQL" })).toBe("");
  });

  it("explains binary, encoding, and file-type rejection", () => {
    expect(sqlReplaceUnavailableReason({ binary: true, encoding: "UTF-8", detected: "binary" })).toMatch(/binary input/);
    expect(sqlReplaceUnavailableReason({ binary: false, encoding: "UTF-16LE", detected: "SQL" })).toMatch(/requires UTF-8/);
    expect(sqlReplaceUnavailableReason({ binary: false, encoding: "UTF-8", detected: "text" })).toMatch(/requires a SQL dump/);
  });

  it("normalizes backend casing", () => {
    expect(sqlReplaceUnavailableReason({ binary: false, encoding: "utf-8", detected: "sql" })).toBe("");
  });
});
