import { describe, expect, it, vi } from "vitest";
import {
  prepareBigFileEditEntry,
  releaseBigFileEditIfClean,
} from "./bigFileEditLifecycle";

describe("large-file edit preparation", () => {
  it("does not load or commit a window when preparation fails or is canceled", async () => {
    const load = vi.fn(async () => "window");
    const commit = vi.fn();
    const release = vi.fn(async () => undefined);

    await expect(
      prepareBigFileEditEntry(
        async () => { throw new Error("canceled"); },
        load,
        commit,
        release,
      ),
    ).rejects.toThrow("canceled");
    expect(load).not.toHaveBeenCalled();
    expect(commit).not.toHaveBeenCalled();
    expect(release).not.toHaveBeenCalled();
  });

  it("commits only after prepare and window load both succeed", async () => {
    const order: string[] = [];
    await prepareBigFileEditEntry(
      async () => {
        order.push("prepare");
        return { editCount: 0 };
      },
      async () => {
        order.push("load");
        return "window";
      },
      () => order.push("commit"),
      async () => { order.push("release"); },
    );
    expect(order).toEqual(["prepare", "load", "commit"]);
  });

  it("releases a clean preparation after window failure but preserves dirty state", async () => {
    const releaseClean = vi.fn(async () => undefined);
    await expect(
      prepareBigFileEditEntry(
        async () => ({ editCount: 0 }),
        async () => { throw new Error("window failed"); },
        vi.fn(),
        releaseClean,
      ),
    ).rejects.toThrow("window failed");
    expect(releaseClean).toHaveBeenCalledOnce();

    const releaseDirty = vi.fn(async () => undefined);
    await expect(
      prepareBigFileEditEntry(
        async () => ({ editCount: 1 }),
        async () => { throw new Error("window failed"); },
        vi.fn(),
        releaseDirty,
      ),
    ).rejects.toThrow("window failed");
    expect(releaseDirty).not.toHaveBeenCalled();
  });

  it("releases only clean edit-mode exits", async () => {
    const release = vi.fn(async () => ({ editCount: 0 }));
    await expect(releaseBigFileEditIfClean({ editCount: 1 }, release)).resolves.toBeNull();
    expect(release).not.toHaveBeenCalled();
    await expect(releaseBigFileEditIfClean({ editCount: 0 }, release)).resolves.toEqual({ editCount: 0 });
    expect(release).toHaveBeenCalledOnce();
  });
});
