import { describe, expect, it, vi } from "vitest";
import { LatestWindowLoader } from "./latestWindowLoader";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

describe("LatestWindowLoader", () => {
  it("loads the latest scroll target after the initial request without parallel scans", async () => {
    const first = deferred<string>();
    const query = vi.fn((offset: number) => offset === 0 ? first.promise : Promise.resolve("target"));
    const loaded = vi.fn();
    const loading = vi.fn();
    const loader = new LatestWindowLoader({ query, loaded, loading, failed: vi.fn() });
    loader.request(0);
    loader.request(1000);
    loader.request(3000);
    expect(query).toHaveBeenCalledTimes(1);
    first.resolve("initial");
    await first.promise;
    await Promise.resolve();
    expect(query.mock.calls.map(([offset]) => offset)).toEqual([0, 3000]);
    expect(loaded).toHaveBeenLastCalledWith("target", 3000);
    expect(loading).toHaveBeenLastCalledWith(false);
  });

  it("ignores stale success and pending requests after a query changes or the view unmounts", async () => {
    const first = deferred<string>();
    const handlers = { query: vi.fn(() => first.promise), loaded: vi.fn(), loading: vi.fn(), failed: vi.fn() };
    const loader = new LatestWindowLoader(handlers);
    loader.request(0);
    loader.request(3000);
    loader.dispose();
    loader.request(6000); // a late debounce callback also belongs to the disposed view
    first.resolve("obsolete");
    await first.promise;
    expect(handlers.query).toHaveBeenCalledTimes(1);
    expect(handlers.loaded).not.toHaveBeenCalled();
    expect(handlers.loading.mock.calls).toEqual([[true]]);
  });

  it("does not publish stale failures and continues from a current failed request", async () => {
    const first = deferred<string>();
    const handlers = {
      query: vi.fn((offset: number) => offset === 0 ? first.promise : Promise.resolve("recovered")),
      loaded: vi.fn(), loading: vi.fn(), failed: vi.fn(),
    };
    const loader = new LatestWindowLoader(handlers);
    loader.request(0);
    loader.request(1000);
    first.reject(new Error("transient failure"));
    await first.promise.catch(() => {});
    await Promise.resolve();
    expect(handlers.failed).toHaveBeenCalledTimes(1);
    expect(handlers.loaded).toHaveBeenLastCalledWith("recovered", 1000);

    const stale = deferred<string>();
    const staleLoader = new LatestWindowLoader({ ...handlers, query: () => stale.promise });
    staleLoader.request(0);
    staleLoader.dispose();
    stale.reject(new Error("obsolete failure"));
    await stale.promise.catch(() => {});
    expect(handlers.failed).toHaveBeenCalledTimes(1);
  });
});
