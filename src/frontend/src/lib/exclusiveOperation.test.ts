import { describe, expect, it } from "vitest";
import { ExclusiveOperation } from "./exclusiveOperation";

describe("ExclusiveOperation", () => {
  it("rejects overlap and only lets the owner release busy state", () => {
    const operation = new ExclusiveOperation();
    const first = operation.tryAcquire("schema");

    expect(first).not.toBeNull();
    expect(operation.tryAcquire("convert")).toBeNull();
    expect(operation.activeLabel()).toBe("schema");
    expect(operation.owns(first!)).toBe(true);
    expect(operation.release({ id: first!.id, label: first!.label })).toBe(false);
    expect(operation.activeLabel()).toBe("schema");
    expect(operation.release(first!)).toBe(true);
    expect(operation.activeLabel()).toBeNull();
  });
});
