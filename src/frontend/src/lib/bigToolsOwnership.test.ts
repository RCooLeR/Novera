import { describe, expect, it } from "vitest";
import {
  BigToolsRequestGate,
  bigToolsFileKind,
  bigToolsSqlAvailability,
  unsupportedBigToolsTypeMessage,
} from "./bigToolsOwnership";

describe("bigToolsFileKind", () => {
  it.each([
    ["CSV", "csv"],
    [" tSv ", "csv"],
    ["sql", "sql"],
    ["JSON", "unsupported"],
    ["", "unsupported"],
  ] as const)("routes backend detection %j to %s", (detected, expected) => {
    expect(bigToolsFileKind(detected)).toBe(expected);
  });

  it("describes unsupported backend detections without inferring from the path", () => {
    expect(unsupportedBigToolsTypeMessage("JSON")).toContain("backend detected JSON");
    expect(unsupportedBigToolsTypeMessage("")).toContain("could not detect a supported type");
  });
});

describe("BigToolsRequestGate", () => {
  it("invalidates every request when the target is replaced, including A to B to A", () => {
    const gate = new BigToolsRequestGate();
    const firstA = gate.beginTarget();
    const openA = gate.begin("open");
    const schemaA = gate.begin("schema");

    gate.beginTarget();
    gate.beginTarget();

    expect(gate.isTargetCurrent(firstA)).toBe(false);
    expect(gate.isCurrent(openA)).toBe(false);
    expect(gate.isCurrent(schemaA)).toBe(false);
  });

  it("orders requests independently within the current target", () => {
    const gate = new BigToolsRequestGate();
    gate.beginTarget();
    const oldSchema = gate.begin("schema");
    const transform = gate.begin("transform");
    const newSchema = gate.begin("schema");

    expect(gate.isCurrent(oldSchema)).toBe(false);
    expect(gate.isCurrent(newSchema)).toBe(true);
    expect(gate.isCurrent(transform)).toBe(true);

    gate.invalidateTarget();
    expect(gate.isCurrent(newSchema)).toBe(false);
    expect(gate.isCurrent(transform)).toBe(false);
  });
});

describe("bigToolsSqlAvailability", () => {
  it("keeps independent SQL transforms available before analysis", () => {
    expect(bigToolsSqlAvailability(false, false)).toEqual({
      independentUnavailable: false,
      analysisDependentUnavailable: true,
    });
    expect(bigToolsSqlAvailability(false, true)).toEqual({
      independentUnavailable: false,
      analysisDependentUnavailable: false,
    });
    expect(bigToolsSqlAvailability(true, true)).toEqual({
      independentUnavailable: true,
      analysisDependentUnavailable: true,
    });
  });
});
