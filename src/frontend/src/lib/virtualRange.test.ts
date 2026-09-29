import { describe, expect, it } from "vitest";
import { virtualRange } from "./virtualRange";

describe("virtualRange", () => {
  it("renders a small filtered result after scrolling deep into the original result", () => {
    expect(virtualRange(3, 100_000, 480, 25, 10)).toEqual({
      start: 0, end: 3, top: 0, topPad: 0, bottomPad: 0,
    });
  });

  it("does not retain a phantom scrollable spacer when a query returns no rows", () => {
    expect(virtualRange(0, 100_000, 480, 25, 10)).toEqual({
      start: 0, end: 0, top: 0, topPad: 0, bottomPad: 0,
    });
  });

  it("keeps the full extent and a bounded DOM window for large results", () => {
    const range = virtualRange(1_000_000, 12_500_000, 500, 25, 10);
    expect(range.end - range.start).toBe(40);
    expect(range.topPad + (range.end - range.start) * 25 + range.bottomPad).toBe(25_000_000);
    expect(range.start).toBeLessThanOrEqual(500_000);
    expect(range.end).toBeGreaterThan(500_000);
  });

  it("expands the rendered window when the containing panel grows", () => {
    expect(virtualRange(1000, 500, 1000, 25, 10).end).toBeGreaterThan(
      virtualRange(1000, 500, 250, 25, 10).end,
    );
  });

  it("allows the final row to scroll fully above the bottom edge with a sticky header", () => {
    const range = virtualRange(1000, 25_000, 500, 25, 10, 30);
    expect(range.top).toBe(24_530);
    expect(range.top + 500).toBe(1000 * 25 + 30);
    expect(range.end).toBe(1000);
  });
});
