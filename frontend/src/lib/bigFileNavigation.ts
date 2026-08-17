export type ParsedBigFileLocation =
  | { kind: "byte"; offset: number }
  | { kind: "line"; line: number }
  | { kind: "error"; message: string };

export interface ExactSearchLocation {
  offset: number;
  length: number;
  line: number;
}

export interface BigFileTextWindow {
  fileId: string;
  startByte: number;
  nextByte: number;
  text: string;
  lineOffsets: number[];
  lineNumbers: number[];
  atBof: boolean;
  atEof: boolean;
  approx: boolean;
}

export interface BigFileHexLine {
  offset: number;
  hex: string;
  ascii: string;
}

export interface BigFileHexWindow {
  fileId: string;
  startByte: number;
  nextByte: number;
  lines: BigFileHexLine[];
  atBof: boolean;
  atEof: boolean;
}

export interface BigFileState {
  size: number;
  modTimeUnixMillis: number;
  sameOpenedFile: boolean;
  changedFromOpen: boolean;
}

export interface BigFileMatchWindow {
  window: BigFileTextWindow;
  found: boolean;
  from: number;
  to: number;
}

export interface BigFileLineResolution {
  offset: number;
  resolvedLine: number;
  exact: boolean;
  found: boolean;
  indexComplete: boolean;
  limited: boolean;
}

export type BigFileLineTarget =
  | { kind: "position"; offset: number; exact: boolean; message: string }
  | { kind: "unavailable"; message: string };

export type ExactSearchLocationResult =
  | { ok: true; location: ExactSearchLocation }
  | { ok: false; message: string };

function exactNonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function exactInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value);
}

function payloadRecord(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} response is malformed.`);
  }
  return value as Record<string, unknown>;
}

function payloadString(value: unknown, field: string): string {
  if (typeof value !== "string") throw new Error(`Large-file response has an invalid ${field}.`);
  return value;
}

function payloadBoolean(value: unknown, field: string): boolean {
  if (typeof value !== "boolean") throw new Error(`Large-file response has an invalid ${field}.`);
  return value;
}

function payloadOffset(value: unknown, field: string): number {
  if (!exactNonNegativeInteger(value)) {
    throw new Error(`Large-file response has an inexact ${field}.`);
  }
  return value;
}

function payloadArray(value: unknown, field: string): unknown[] {
  // Go nil slices can arrive as null. Treat them as empty, but continue to
  // enforce all cross-field row-count invariants below.
  if (value === null || value === undefined) return [];
  if (!Array.isArray(value)) throw new Error(`Large-file response has an invalid ${field}.`);
  return value;
}

function validateWindowBounds(startByte: number, nextByte: number): void {
  if (nextByte < startByte) {
    throw new Error("Large-file response moves backwards.");
  }
}

/**
 * Validates the runtime bridge payload before it can replace the visible file
 * window. The file identity check prevents a late response for another open
 * document from being rendered under the current tab.
 */
export function normalizeBigFileTextWindow(
  value: unknown,
  expectedFileId: string,
  rowLimit = 2_000,
): BigFileTextWindow {
  const payload = payloadRecord(value, "Large-file window");
  const fileId = payloadString(payload.fileId, "file identity");
  if (fileId !== expectedFileId) {
    throw new Error("Large-file window belongs to a different file.");
  }

  const startByte = payloadOffset(payload.startByte, "window start");
  const nextByte = payloadOffset(payload.nextByte, "window continuation");
  validateWindowBounds(startByte, nextByte);

  const text = payloadString(payload.text, "window text");
  if (text.length > 8 * 1024 * 1024) {
    throw new Error("Large-file response contains too much decoded text.");
  }
  const rawOffsets = payloadArray(payload.lineOffsets, "line offsets");
  const rawNumbers = payloadArray(payload.lineNumbers, "line numbers");
  if (rawOffsets.length !== rawNumbers.length) {
    throw new Error("Large-file response line offset/number counts do not match.");
  }
  if (!Number.isSafeInteger(rowLimit) || rowLimit < 0 || rawOffsets.length > rowLimit) {
    throw new Error("Large-file response contains too many rows.");
  }

  const lineOffsets = rawOffsets.map((offset, index) =>
    payloadOffset(offset, `line offset ${index + 1}`),
  );
  const lineNumbers = rawNumbers.map((line, index) => {
    if (typeof line !== "number" || !Number.isSafeInteger(line) || line < 1) {
      throw new Error(`Large-file response has an inexact line number ${index + 1}.`);
    }
    return line;
  });

  const textRowCount = text === "" ? (lineOffsets.length === 0 ? 0 : 1) : text.split("\n").length;
  if (textRowCount !== lineOffsets.length) {
    throw new Error("Large-file response text/coordinate row counts do not match.");
  }

  return {
    fileId,
    startByte,
    nextByte,
    text,
    lineOffsets,
    lineNumbers,
    atBof: payloadBoolean(payload.atBof, "beginning-of-file flag"),
    atEof: payloadBoolean(payload.atEof, "end-of-file flag"),
    approx: payloadBoolean(payload.approx, "line-number precision flag"),
  };
}

/**
 * Requires a previous-page response to end exactly at the requested viewport
 * anchor. Silently accepting a short response would prepend a gap while making
 * the UI believe the viewport is contiguous.
 */
export function requireAdjacentPreviousTextWindow(
  window: BigFileTextWindow,
  currentStart: number,
): BigFileTextWindow {
  if (!exactNonNegativeInteger(currentStart)) {
    throw new Error("Previous-window anchor is outside the exact byte-navigation range.");
  }
  if (window.nextByte !== currentStart) {
    throw new Error(
      `Previous large-file window is not adjacent to the viewport (${window.nextByte} != ${currentStart}).`,
    );
  }
  if (window.startByte > currentStart) {
    throw new Error("Previous large-file window starts after the viewport anchor.");
  }
  return window;
}

/**
 * Validates a follow-tail poll before its byte size can become a viewport
 * anchor. Unix milliseconds may be negative, but must remain an exact integer.
 */
export function normalizeBigFileState(value: unknown): BigFileState {
  const payload = payloadRecord(value, "Large-file state");
  const modTimeUnixMillis = payload.modTimeUnixMillis;
  if (!exactInteger(modTimeUnixMillis)) {
    throw new Error("Large-file response has an inexact modification time.");
  }
  return {
    size: payloadOffset(payload.size, "file size"),
    modTimeUnixMillis,
    sameOpenedFile: payloadBoolean(payload.sameOpenedFile, "opened-file identity flag"),
    changedFromOpen: payloadBoolean(payload.changedFromOpen, "source-change flag"),
  };
}

/**
 * Validates the bounded match window and exact UTF-16 coordinates before the
 * renderer slices a JavaScript string for highlighting.
 */
export function normalizeBigFileMatchWindow(
  value: unknown,
  expectedFileId: string,
): BigFileMatchWindow {
  const payload = payloadRecord(value, "Large-file match window");
  const window = normalizeBigFileTextWindow(payload.window, expectedFileId);
  const found = payloadBoolean(payload.found, "match-window found flag");
  const from = payloadOffset(payload.from, "match start");
  const to = payloadOffset(payload.to, "match end");
  if (to < from || to > window.text.length) {
    throw new Error("Large-file match coordinates are outside the decoded window.");
  }
  if (!found && (from !== 0 || to !== 0)) {
    throw new Error("Large-file response exposes coordinates for an unavailable match.");
  }
  return { window, found, from, to };
}

/**
 * Applies the same file-identity, exact-number, and bounded-row checks to hex
 * responses before they can replace the visible window.
 */
export function normalizeBigFileHexWindow(
  value: unknown,
  expectedFileId: string,
  rowLimit = 4_096,
): BigFileHexWindow {
  const payload = payloadRecord(value, "Large-file hex window");
  const fileId = payloadString(payload.fileId, "file identity");
  if (fileId !== expectedFileId) {
    throw new Error("Large-file hex window belongs to a different file.");
  }
  const startByte = payloadOffset(payload.startByte, "hex window start");
  const nextByte = payloadOffset(payload.nextByte, "hex window continuation");
  validateWindowBounds(startByte, nextByte);

  const rawLines = payloadArray(payload.lines, "hex lines");
  if (!Number.isSafeInteger(rowLimit) || rowLimit < 0 || rawLines.length > rowLimit) {
    throw new Error("Large-file hex response contains too many rows.");
  }
  const lines = rawLines.map((rawLine, index) => {
    const line = payloadRecord(rawLine, `Large-file hex row ${index + 1}`);
    return {
      offset: payloadOffset(line.offset, `hex row offset ${index + 1}`),
      hex: payloadString(line.hex, `hex row ${index + 1}`),
      ascii: payloadString(line.ascii, `ASCII row ${index + 1}`),
    };
  });

  return {
    fileId,
    startByte,
    nextByte,
    lines,
    atBof: payloadBoolean(payload.atBof, "hex beginning-of-file flag"),
    atEof: payloadBoolean(payload.atEof, "hex end-of-file flag"),
  };
}

/**
 * Validates the structured line-resolution bridge payload. Offset zero is a
 * valid position whenever found=true; absence and pending-index states are
 * represented by separate flags and never inferred from the numeric offset.
 */
export function normalizeBigFileLineResolution(value: unknown): BigFileLineResolution {
  const payload = payloadRecord(value, "Large-file line lookup");
  const resolution: BigFileLineResolution = {
    offset: payloadOffset(payload.offset, "resolved byte offset"),
    resolvedLine: payloadOffset(payload.resolvedLine, "resolved line number"),
    exact: payloadBoolean(payload.exact, "exact-line flag"),
    found: payloadBoolean(payload.found, "known-position flag"),
    indexComplete: payloadBoolean(payload.indexComplete, "line-index completion flag"),
    limited: payloadBoolean(payload.limited, "bounded-scan flag"),
  };

  if (resolution.found) {
    if (resolution.resolvedLine < 1) {
      throw new Error("Large-file line lookup has no line for its known position.");
    }
    if (resolution.exact && resolution.limited) {
      throw new Error("Large-file line lookup is both exact and scan-limited.");
    }
    if (!resolution.exact && resolution.indexComplete !== resolution.limited) {
      throw new Error("Large-file line lookup has an inconsistent fallback state.");
    }
  } else {
    if (resolution.offset !== 0 || resolution.resolvedLine !== 0 || resolution.exact) {
      throw new Error("Large-file line lookup exposes a position that was not found.");
    }
    if (resolution.limited && !resolution.indexComplete) {
      throw new Error("Large-file line lookup is limited before its index is complete.");
    }
  }

  return resolution;
}

/**
 * Converts a validated backend resolution into either an exact/fallback
 * navigation target or an honest unavailable message for the go-to UI.
 */
export function bigFileLineTarget(value: unknown, requestedLine: number): BigFileLineTarget {
  if (!Number.isSafeInteger(requestedLine) || requestedLine < 1) {
    throw new Error("Requested line number is outside the exact supported range.");
  }
  const resolution = normalizeBigFileLineResolution(value);
  if (!resolution.found) {
    if (resolution.limited) {
      return {
        kind: "unavailable",
        message: "Exact line lookup reached its bounded scan limit before a usable known position was available.",
      };
    }
    return {
      kind: "unavailable",
      message: resolution.indexComplete ? "Line does not exist." : "Line index is not ready yet.",
    };
  }
  if (resolution.resolvedLine > requestedLine) {
    throw new Error("Large-file line lookup returned a position after the requested line.");
  }
  if (resolution.exact) {
    if (resolution.resolvedLine !== requestedLine) {
      throw new Error("Large-file line lookup marked the wrong line as exact.");
    }
    return { kind: "position", offset: resolution.offset, exact: true, message: "" };
  }

  const known = `nearest known line ${resolution.resolvedLine.toLocaleString()}`;
  return {
    kind: "position",
    offset: resolution.offset,
    exact: false,
    message: resolution.limited
      ? `Exact line lookup reached its bounded scan limit; using ${known} instead.`
      : `Using ${known} while indexing continues.`,
  };
}

/**
 * Parses the large-file go-to field without allowing partial numeric parses,
 * NaN, or byte positions that JavaScript cannot represent exactly.
 */
export function parseBigFileLocation(value: string, fileSize: number): ParsedBigFileLocation {
  const input = value.trim();
  if (!input) return { kind: "error", message: "Enter a line number, 0xHEX offset, or NN%." };

  const percentage = input.match(/^(\d{1,3}(?:\.\d+)?)\s*%$/);
  if (percentage) {
    const amount = Number(percentage[1]);
    if (!Number.isFinite(amount) || amount < 0 || amount > 100) {
      return { kind: "error", message: "Percentage must be between 0 and 100." };
    }
    if (!exactNonNegativeInteger(fileSize)) {
      return {
        kind: "error",
        message: "This file size is outside the exact byte-navigation range.",
      };
    }
    const offset = Math.floor((fileSize * amount) / 100);
    return exactNonNegativeInteger(offset)
      ? { kind: "byte", offset }
      : { kind: "error", message: "Byte offset is outside the exact supported range." };
  }

  if (/^0x[0-9a-f]+$/i.test(input)) {
    const offset = Number.parseInt(input.slice(2), 16);
    return exactNonNegativeInteger(offset)
      ? { kind: "byte", offset }
      : { kind: "error", message: "Byte offset is outside the exact supported range." };
  }

  if (/^\d+$/.test(input)) {
    const line = Number(input);
    return Number.isSafeInteger(line) && line >= 1
      ? { kind: "line", line }
      : { kind: "error", message: "Line number is outside the exact supported range." };
  }

  return { kind: "error", message: "Enter a line number, 0xHEX offset, or NN%." };
}

/**
 * Rejects imprecise bridge numbers before they are reused as byte anchors.
 */
export function exactSearchLocation(
  offset: unknown,
  length: unknown,
  line: unknown,
): ExactSearchLocationResult {
  if (!exactNonNegativeInteger(offset)) {
    return { ok: false, message: "Search returned an inexact byte offset." };
  }
  if (!exactNonNegativeInteger(length) || !Number.isSafeInteger(offset + length)) {
    return { ok: false, message: "Search returned an inexact match length." };
  }
  if (!exactNonNegativeInteger(line)) {
    return { ok: false, message: "Search returned an inexact line number." };
  }
  return { ok: true, location: { offset, length, line } };
}
