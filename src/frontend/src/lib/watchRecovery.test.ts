import { describe, expect, it, vi } from "vitest";
import { isRecoverableWatchError, WatchRecoveryCoordinator } from "./watchRecovery";

const flush = () => new Promise<void>((resolve) => queueMicrotask(resolve));

describe("WatchRecoveryCoordinator", () => {
  it("distinguishes retired-backend failures from ordinary path errors", () => {
    expect(isRecoverableWatchError("file watcher runtime failure: event queue overflow")).toBe(true);
    expect(isRecoverableWatchError("file watcher unavailable: watcher reconstruction failed")).toBe(true);
    expect(isRecoverableWatchError('watch "bad.txt": path escapes workspace')).toBe(false);
  });

  it("coalesces an error burst into one successful recovery", async () => {
    const coordinator = new WatchRecoveryCoordinator();
    const sync = vi.fn().mockResolvedValue(true);

    expect(coordinator.request(sync)).toBe(true);
    expect(coordinator.request(sync)).toBe(false);
    await flush();
    await flush();

    expect(sync).toHaveBeenCalledOnce();
    expect(coordinator.recoveryState()).toBe("idle");
    expect(coordinator.request(sync)).toBe(true);
  });

  it("blocks recursive retries after failure until a later sync succeeds", async () => {
    const coordinator = new WatchRecoveryCoordinator();
    const sync = vi.fn().mockResolvedValue(false);

    expect(coordinator.request(sync)).toBe(true);
    await flush();
    await flush();
    expect(coordinator.recoveryState()).toBe("blocked");
    expect(coordinator.request(sync)).toBe(false);

    coordinator.observeSyncSuccess();
    expect(coordinator.request(sync)).toBe(true);
  });

  it("re-arms when the workspace identity changes", async () => {
    const coordinator = new WatchRecoveryCoordinator();
    expect(coordinator.request(() => Promise.resolve(false))).toBe(true);
    await flush();
    await flush();
    expect(coordinator.recoveryState()).toBe("blocked");

    coordinator.reset();
    expect(coordinator.recoveryState()).toBe("idle");
  });
});
