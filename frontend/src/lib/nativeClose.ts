export interface NativeCloseRequest {
  nonce: string;
}

export interface NativeCloseDecision extends NativeCloseRequest {
  hasUnsavedResources: boolean;
}

export type NativeCloseOutcome = "authorized" | "blocked" | "error" | "invalid";

interface NativeCloseDependencies {
  hasUnsavedResources: () => boolean;
  emitDecision: (decision: NativeCloseDecision) => Promise<unknown>;
  setStatus: (message: string, kind: "error") => void;
}

function eventPayload(data: unknown): unknown {
  return Array.isArray(data) ? (data.length === 1 ? data[0] : null) : data;
}

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  return typeof error === "string" && error ? error : "unknown bridge error";
}

export function parseNativeCloseRequest(data: unknown): NativeCloseRequest | null {
  const payload = eventPayload(data);
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) return null;
  const nonce = (payload as Record<string, unknown>).nonce;
  // The host generates 128-bit nonces as lowercase hexadecimal. Rejecting any
  // other shape avoids reflecting malformed native event data back as a close
  // authorization attempt.
  return typeof nonce === "string" && /^[0-9a-f]{32}$/.test(nonce) ? { nonce } : null;
}

// Answer a native close request from the live Zustand snapshot. This helper
// intentionally has no dependency on the asynchronous SetUnsavedResources
// mirror: even an edit immediately followed by title-bar close is observed.
export async function handleNativeCloseRequest(
  data: unknown,
  dependencies: NativeCloseDependencies,
): Promise<NativeCloseOutcome> {
  const request = parseNativeCloseRequest(data);
  if (!request) {
    dependencies.setStatus("Ignored a malformed native close request; Novera will remain open.", "error");
    return "invalid";
  }

  const hasUnsavedResources = dependencies.hasUnsavedResources();
  try {
    await dependencies.emitDecision({ ...request, hasUnsavedResources });
  } catch (error: unknown) {
    dependencies.setStatus(
      `Could not verify whether Novera can close: ${errorMessage(error)}. Novera will remain open.`,
      "error",
    );
    return "error";
  }

  if (hasUnsavedResources) {
    dependencies.setStatus("Save or explicitly discard all unsaved changes before closing Novera.", "error");
    return "blocked";
  }
  return "authorized";
}
