import { describe, expect, it } from "vitest";
import { bigFileJobProgressLabel, reduceBigFileJobEvent, type ActiveBigFileJob } from "./bigFileJobState";

const start = (id = "job1", sequence = 1) => ({
  id,
  sequence,
  title: "Transform",
  kind: "transform",
  fileId: "f1",
  completed: 0,
  total: 10,
  note: "",
});

describe("reduceBigFileJobEvent", () => {
  it("rejects missing, stale, equal, and conflicting job identities", () => {
    const current = reduceBigFileJobEvent(null, "start", start())!;
    expect(reduceBigFileJobEvent(current, "start", { ...start("job2", 0) })).toBe(current);
    expect(reduceBigFileJobEvent(current, "start", start("job2", 1))).toBe(current);
    expect(reduceBigFileJobEvent(current, "start", start("job1", 2))).toBe(current);
    expect(reduceBigFileJobEvent(current, "progress", { id: "job2", sequence: 2, completed: 9 })).toBe(current);
    expect(reduceBigFileJobEvent(current, "end", { id: "job2", sequence: 2 })).toBe(current);
  });

  it("accepts a strictly newer start and ignores the predecessor's late end", () => {
    const first = reduceBigFileJobEvent(null, "start", start())!;
    const second = reduceBigFileJobEvent(first, "start", start("job2", 2))!;
    expect(second.id).toBe("job2");
    expect(reduceBigFileJobEvent(second, "end", { id: "job1", sequence: 1 })).toBe(second);
  });

  it("keeps progress monotonic, bounded, and sequence-owned", () => {
    const current = reduceBigFileJobEvent(null, "start", start())!;
    const progressed = reduceBigFileJobEvent(current, "progress", {
      id: "job1",
      sequence: 1,
      completed: 99,
      total: 10,
      note: "done",
    })!;
    expect(progressed.completed).toBe(10);
    expect(reduceBigFileJobEvent(progressed, "progress", { id: "job1", sequence: 1, completed: 2 })?.completed).toBe(10);
    expect(reduceBigFileJobEvent(progressed, "progress", { id: "job1", sequence: 2, completed: 1 })).toBe(progressed);
  });

  it("clears only an exact terminal event", () => {
    const current = reduceBigFileJobEvent(null, "start", start())!;
    expect(reduceBigFileJobEvent(current, "end", { id: "job1", sequence: 1 })).toBeNull();
  });
});

describe("bigFileJobProgressLabel", () => {
  it("formats determinate records and byte-oriented analysis", () => {
    const base: ActiveBigFileJob = { ...start(), note: "" };
    expect(bigFileJobProgressLabel({ ...base, completed: 5 })).toContain("50%");
    expect(bigFileJobProgressLabel({ ...base, kind: "sql-analysis", completed: 1024, total: 2048 })).toContain("KiB");
  });
});
