import { describe, expect, it, vi } from "vitest";
import { writeClipboardText } from "./clipboard";

describe("writeClipboardText", () => {
  it("awaits a successful clipboard write", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    await expect(writeClipboardText("copied", { writeText })).resolves.toBeUndefined();
    expect(writeText).toHaveBeenCalledWith("copied");
  });

  it("reports unavailable and rejected clipboard access", async () => {
    await expect(writeClipboardText("copied", undefined)).rejects.toThrow("Clipboard access is not available");
    await expect(
      writeClipboardText("copied", { writeText: vi.fn().mockRejectedValue(new Error("permission denied")) }),
    ).rejects.toThrow("Could not copy to the clipboard: permission denied");
  });
});
