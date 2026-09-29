import { describe, expect, it, vi } from "vitest";
import { BigFileSearchRequestOwner } from "./bigFileSearchRequest";

describe("BigFileSearchRequestOwner", () => {
  it("cancels only the exact superseded identity and never reuses it", () => {
    const cancel = vi.fn();
    const owner = new BigFileSearchRequestOwner(cancel);

    owner.activate("search1");
    owner.activate("search2");
    owner.settle("search1");

    expect(cancel).toHaveBeenCalledTimes(1);
    expect(cancel).toHaveBeenCalledWith("search1");
    expect(owner.activeRequestId).toBe("search2");

    owner.cancel();
    owner.cancel();
    expect(cancel).toHaveBeenCalledTimes(2);
    expect(cancel).toHaveBeenLastCalledWith("search2");
  });

  it("rejects malformed bridge identities before they become cancellation owners", () => {
    const owner = new BigFileSearchRequestOwner(vi.fn());
    expect(() => owner.activate("search0")).toThrow(/invalid search request identity/i);
    expect(() => owner.activate("not-search1")).toThrow(/invalid search request identity/i);
    expect(owner.activeRequestId).toBe("");
  });
});
