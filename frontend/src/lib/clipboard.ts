export interface ClipboardWriter {
  writeText(text: string): Promise<void>;
}

export async function writeClipboardText(text: string, clipboard?: ClipboardWriter): Promise<void> {
  const writer = clipboard ?? (typeof navigator !== "undefined" ? navigator.clipboard : undefined);
  if (!writer?.writeText) throw new Error("Clipboard access is not available in this environment.");
  try {
    await writer.writeText(text);
  } catch (error) {
    const detail = error instanceof Error && error.message ? `: ${error.message}` : "";
    throw new Error(`Could not copy to the clipboard${detail}`, { cause: error });
  }
}
