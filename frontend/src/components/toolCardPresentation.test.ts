import { describe, expect, it } from "vitest";
import { toolCardPresentation } from "./toolCardPresentation";

describe("toolCardPresentation", () => {
  it.each([
    ["write_file", '{"path":"report.txt","content":1}', "content"],
    ["http_request", '{"url":"https://example.test","body":{}}', "body"],
    ["apply_edit", '{"path":"file.txt","oldText":[],"newText":"safe"}', "oldText"],
  ])("safely rejects non-string display arguments for %s", (tool, raw, field) => {
    const presentation = toolCardPresentation(tool, raw);

    expect(presentation.argumentError).toContain(field);
    expect(presentation.approvalSafe).toBe(false);
    expect(presentation.detail).toBeNull();
  });

  it.each(["not-json", "[]", "null", '"arguments"'])('safely rejects a non-object payload: %s', (raw) => {
    expect(() => toolCardPresentation("write_file", raw)).not.toThrow();
    expect(toolCardPresentation("write_file", raw).approvalSafe).toBe(false);
  });

  it("keeps valid numeric fields that are not rendered while measuring only strings", () => {
    const presentation = toolCardPresentation("db_query", '{"sql":"SELECT 1","limit":100}');

    expect(presentation).toMatchObject({
      summary: "SELECT 1",
      detail: "SELECT 1",
      argumentError: null,
      approvalSafe: true,
    });
  });
});
