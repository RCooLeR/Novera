export interface SQLReplaceSourceMeta {
  binary: boolean;
  encoding: string;
  detected: string;
}

export function sqlReplaceUnavailableReason(meta: SQLReplaceSourceMeta): string {
  if (meta.binary) return "Find and replace is unavailable for binary input.";
  if (meta.detected.toUpperCase() !== "SQL") {
    return `Find and replace requires a SQL dump; this file was detected as ${meta.detected || "unknown"}.`;
  }
  if (meta.encoding.toUpperCase() !== "UTF-8") {
    return `Find and replace currently requires UTF-8; this dump was detected as ${meta.encoding || "unknown encoding"}.`;
  }
  return "";
}
