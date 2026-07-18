import type { Job } from "../lib/services";

export function durationAt(job: Job, now: number): string {
  const end = job.endedAt || now;
  const ms = Math.max(0, end - job.startedAt);
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export interface JobLogState {
  jobId: string;
  lines: string[];
  loading: boolean;
  error: string;
}

export function visibleJobLog(jobId: string, state: JobLogState): JobLogState {
  if (state.jobId === jobId) return state;
  return { jobId, lines: [], loading: true, error: "" };
}
