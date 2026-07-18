import { describe, expect, it } from "vitest";
import { nextLinearIndex } from "./keyboardNavigation";

describe("nextLinearIndex", () => {
  it("wraps forward and backward navigation", () => {
    expect(nextLinearIndex(2, 3, "ArrowRight")).toBe(0);
    expect(nextLinearIndex(0, 3, "ArrowLeft")).toBe(2);
    expect(nextLinearIndex(1, 3, "ArrowDown")).toBe(2);
    expect(nextLinearIndex(1, 3, "ArrowUp")).toBe(0);
  });

  it("supports Home and End and handles empty collections", () => {
    expect(nextLinearIndex(1, 3, "Home")).toBe(0);
    expect(nextLinearIndex(1, 3, "End")).toBe(2);
    expect(nextLinearIndex(0, 0, "ArrowRight")).toBe(-1);
  });
});
