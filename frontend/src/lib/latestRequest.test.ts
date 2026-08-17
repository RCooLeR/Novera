import { describe, expect, it } from "vitest";
import { LatestRequest } from "./latestRequest";

describe("LatestRequest", () => {
  it("lets only the latest generation commit", () => {
    const requests = new LatestRequest();
    const older = requests.begin();
    const newer = requests.begin();

    expect(requests.isCurrent(older)).toBe(false);
    expect(requests.isCurrent(newer)).toBe(true);
  });

  it("invalidates an outstanding generation during cleanup", () => {
    const requests = new LatestRequest();
    const pending = requests.begin();

    requests.invalidate();

    expect(requests.isCurrent(pending)).toBe(false);
  });

  it("does not revive an old request across an A to B to A context change", () => {
    const requests = new LatestRequest();
    const firstA = requests.begin();

    requests.invalidate(); // A -> B
    requests.invalidate(); // B -> A before the next request begins
    const secondA = requests.begin();

    expect(requests.isCurrent(firstA)).toBe(false);
    expect(requests.isCurrent(secondA)).toBe(true);
  });
});
