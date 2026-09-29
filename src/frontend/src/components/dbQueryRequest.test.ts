import { describe, expect, it } from "vitest";
import { DbQueryRequestOwner } from "./dbQueryRequest";

describe("DbQueryRequestOwner", () => {
  it("rejects a second query synchronously before React can commit busy state", () => {
    const owner = new DbQueryRequestOwner();
    const first = owner.tryBegin();

    expect(first).not.toBeNull();
    expect(owner.tryBegin()).toBeNull();
    expect(owner.isCurrent(first!)).toBe(true);
  });

  it("does not let an obsolete completion publish or clear a newer request", () => {
    const owner = new DbQueryRequestOwner();
    const obsolete = owner.tryBegin()!;

    owner.invalidate();
    const current = owner.tryBegin()!;

    expect(owner.isCurrent(obsolete)).toBe(false);
    expect(owner.finish(obsolete)).toBe(false);
    expect(owner.isCurrent(current)).toBe(true);
    expect(owner.finish(current)).toBe(true);
  });
});
