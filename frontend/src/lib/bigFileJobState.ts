export interface ActiveBigFileJob {
  id: string;
  sequence: number;
  title: string;
  kind: string;
  fileId: string;
  completed: number;
  total: number;
  note: string;
}

export type BigFileJobEventType = "start" | "progress" | "end";

const MAX_SAFE_PROGRESS = Number.MAX_SAFE_INTEGER;
const MAX_NOTE_LENGTH = 160;

function text(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function count(value: unknown, fallback = 0): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return fallback;
  return Math.min(MAX_SAFE_PROGRESS, Math.max(0, Math.trunc(value)));
}

function eventSequence(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : null;
}

function note(value: unknown, fallback = ""): string {
  return text(value, fallback).slice(0, MAX_NOTE_LENGTH);
}

// reduceBigFileJobEvent makes the backend job ID and sequence authoritative.
// Delayed progress/end events for job A cannot mutate or clear successor B.
export function reduceBigFileJobEvent(
  current: ActiveBigFileJob | null,
  type: BigFileJobEventType,
  payload: Record<string, unknown>,
): ActiveBigFileJob | null {
  const id = text(payload.id).trim();
  if (!id) return current;

  if (type === "start") {
    const sequence = eventSequence(payload.sequence);
    if (sequence == null) return current;
    if (current) {
      if (current.id !== id) {
        if (sequence <= current.sequence) return current;
      } else if (sequence !== current.sequence) {
        return current;
      }
    }
    return {
      id,
      sequence,
      title: text(payload.title, "Working") || "Working",
      kind: text(payload.kind, "transform") || "transform",
      fileId: text(payload.fileId),
      completed: count(payload.completed, count(payload.records)),
      total: count(payload.total),
      note: note(payload.note),
    };
  }

  if (!current || current.id !== id) return current;
  if (Object.prototype.hasOwnProperty.call(payload, "sequence")) {
    const sequence = eventSequence(payload.sequence);
    if (sequence == null || sequence !== current.sequence) return current;
  }
  if (type === "end") return null;

  const total = Math.max(current.total, count(payload.total, current.total));
  let completed = Math.max(current.completed, count(payload.completed, count(payload.records, current.completed)));
  if (total > 0) completed = Math.min(completed, total);
  return {
    ...current,
    completed,
    total,
    note: note(payload.note, current.note),
  };
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
  let scaled = value / 1024;
  let index = 0;
  while (scaled >= 1024 && index < units.length - 1) {
    scaled /= 1024;
    index++;
  }
  return `${scaled.toFixed(scaled < 10 ? 1 : 0)} ${units[index]}`;
}

export function bigFileJobProgressLabel(job: ActiveBigFileJob): string {
  const format =
    job.kind === "sql-analysis" || job.kind === "source-verification"
      ? formatBytes
      : (value: number) => value.toLocaleString();
  if (job.total > 0) {
    const percent = Math.min(100, Math.floor((job.completed / job.total) * 100));
    return `${format(job.completed)} / ${format(job.total)} (${percent}%)`;
  }
  if (job.completed > 0) return format(job.completed);
  return "Starting…";
}
