import { describe, expect, it } from "vitest";
import {
  activeResourceCapabilities,
  resourceCapabilities,
  resourceCommandEligibility,
  type ResourceDescriptor,
} from "./resourceCapabilities";

function resource(patch: Partial<ResourceDescriptor> = {}): ResourceDescriptor {
  return {
    path: "notes.txt",
    kind: "file",
    binary: false,
    tooLarge: false,
    ...patch,
  };
}

describe("active resource capabilities", () => {
  it("enables ordinary editor commands only for editable real files", () => {
    const capabilities = resourceCapabilities(resource());
    expect(capabilities).toMatchObject({
      path: "notes.txt",
      realFile: true,
      editable: true,
      csv: false,
      dump: false,
      diff: false,
      database: false,
      table: false,
      largeFile: false,
      binary: false,
    });
    expect(resourceCommandEligibility(capabilities, false)).toMatchObject({
      save: true,
      formatDocument: true,
      saveArtifact: true,
    });
  });

  it.each([
    ["diff", "diff:u:exports/data.csv"],
    ["db", "db:connection-id"],
    ["table", "exports/data.csv"],
  ] as const)("never treats a %s tab's path-like key as a file", (kind, path) => {
    const capabilities = resourceCapabilities(resource({ kind, path }));
    expect(capabilities).toMatchObject({
      path: null,
      realFile: false,
      editable: false,
      csv: false,
      dump: false,
    });
    expect(resourceCommandEligibility(capabilities, false)).toEqual({
      save: false,
      formatDocument: false,
      inferCsvSchema: false,
      csvToSql: false,
      analyzeDump: false,
      cleanDump: false,
      dataTools: false,
      saveArtifact: false,
    });
  });

  it("keeps large CSV files backend-addressable without claiming they are editor-editable", () => {
    const capabilities = resourceCapabilities(resource({ path: "exports/huge.csv", tooLarge: true }));
    expect(capabilities).toMatchObject({ realFile: true, editable: false, csv: true, largeFile: true });
    expect(resourceCommandEligibility(capabilities, false)).toMatchObject({
      save: false,
      inferCsvSchema: true,
      csvToSql: true,
      dataTools: true,
      saveArtifact: true,
    });
  });

  it("does not expose data-file commands for a binary file with a misleading extension", () => {
    const capabilities = resourceCapabilities(resource({ path: "payload.csv", binary: true }));
    expect(capabilities).toMatchObject({ realFile: true, editable: false, csv: false, binary: true });
    expect(resourceCommandEligibility(capabilities, false).saveArtifact).toBe(true);
  });

  it.each(["../escape.csv", "/absolute.csv", "C:\\absolute.csv", "C:drive-relative.csv", "dir/../escape.sql", "dir/"])(
    "rejects non-file workspace path %s",
    (path) => {
      expect(resourceCapabilities(resource({ path }))).toMatchObject({
        path: null,
        realFile: false,
        csv: false,
        dump: false,
      });
    },
  );

  it("resolves capabilities from the selected tab instead of the active key's suffix", () => {
    const tabs = [resource({ path: "real.sql" }), resource({ kind: "diff", path: "diff:u:fake.csv" })];
    expect(activeResourceCapabilities(tabs, "diff:u:fake.csv")).toMatchObject({ diff: true, csv: false, realFile: false });
    expect(activeResourceCapabilities(tabs, "missing.csv")).toMatchObject({ kind: null, csv: false, realFile: false });
  });

  it("includes tool busy state in command eligibility", () => {
    const capabilities = resourceCapabilities(resource({ path: "data.dump" }));
    expect(resourceCommandEligibility(capabilities, true)).toMatchObject({
      analyzeDump: false,
      cleanDump: false,
      dataTools: true,
    });
  });
});
