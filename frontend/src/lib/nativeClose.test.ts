import { describe, expect, it, vi } from "vitest";
import { handleNativeCloseRequest, parseNativeCloseRequest, type NativeCloseDecision } from "./nativeClose";

const nonce = "a".repeat(32);

describe("native close handshake", () => {
  it("reads the live dirty state even when the asynchronous native mirror is still clean", async () => {
    let asynchronouslyMirroredDirty = false;
    const decisions: NativeCloseDecision[] = [];
    const setStatus = vi.fn();

    // Model an edit whose queued SetUnsavedResources(true) has not completed.
    const liveDirtyState = true;
    expect(asynchronouslyMirroredDirty).toBe(false);
    const outcome = await handleNativeCloseRequest({ nonce }, {
      hasUnsavedResources: () => liveDirtyState,
      emitDecision: async (decision) => {
        decisions.push(decision);
        asynchronouslyMirroredDirty = true;
      },
      setStatus,
    });

    expect(outcome).toBe("blocked");
    expect(decisions).toEqual([{ nonce, hasUnsavedResources: true }]);
    expect(setStatus).toHaveBeenCalledWith(expect.stringContaining("unsaved changes"), "error");
  });

  it("authorizes only by returning the exact nonce with a current clean decision", async () => {
    const emitDecision = vi.fn().mockResolvedValue(undefined);
    const setStatus = vi.fn();

    await expect(handleNativeCloseRequest([{ nonce }], {
      hasUnsavedResources: () => false,
      emitDecision,
      setStatus,
    })).resolves.toBe("authorized");

    expect(emitDecision).toHaveBeenCalledWith({ nonce, hasUnsavedResources: false });
    expect(setStatus).not.toHaveBeenCalled();
  });

  it("fails closed when sending the renderer decision errors", async () => {
    const setStatus = vi.fn();

    await expect(handleNativeCloseRequest({ nonce }, {
      hasUnsavedResources: () => false,
      emitDecision: vi.fn().mockRejectedValue(new Error("bridge unavailable")),
      setStatus,
    })).resolves.toBe("error");

    expect(setStatus).toHaveBeenCalledWith(expect.stringMatching(/bridge unavailable.*remain open/), "error");
  });

  it.each([
    null,
    {},
    { nonce: "stale-short-nonce" },
    { nonce: "A".repeat(32) },
    [{ nonce }, { nonce }],
  ])("rejects malformed request payload %#", (payload) => {
    expect(parseNativeCloseRequest(payload)).toBeNull();
  });
});
