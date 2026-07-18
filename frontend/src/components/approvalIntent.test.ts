import { describe, expect, it } from "vitest";
import { approvalIntentPages, formatApprovalIntent } from "./approvalIntent";

describe("approval intent rendering", () => {
  it("makes every character reachable without clipping a dangerous tail", () => {
    const raw = JSON.stringify({ tool: "write_file", args: { content: `prefix-${"x".repeat(50)}-dangerous-tail` } });
    const formatted = formatApprovalIntent(raw);
    const pages = approvalIntentPages(raw, 17);

    expect(pages.length).toBeGreaterThan(1);
    expect(pages.join("")).toBe(formatted);
    expect(pages.join("")).toContain("dangerous-tail");
  });

  it("preserves malformed input verbatim so a protocol error is visible", () => {
    const raw = "not-json\nwith-tail";
    expect(approvalIntentPages(raw, 4).join("")).toBe(raw);
  });
});
