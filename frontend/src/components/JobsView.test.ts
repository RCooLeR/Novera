import { describe, expect, it } from "vitest";
import type { Job } from "../lib/services";
import { durationAt, visibleJobLog } from "./jobsViewState";

describe("JobsView state helpers", () => {
  it("never exposes a previous job's log under a newly selected job", () => {
    const previous = { jobId: "job-a", lines: ["secret output from A"], loading: false, error: "" };
    expect(visibleJobLog("job-b", previous)).toEqual({
      jobId: "job-b",
      lines: [],
      loading: true,
      error: "",
    });
  });

  it("ticks running durations from the supplied render time", () => {
    const job = { startedAt: 1_000, endedAt: 0 } as Job;
    expect(durationAt(job, 3_000)).toBe("2s");
    expect(durationAt(job, 63_000)).toBe("1m 2s");
  });
});
