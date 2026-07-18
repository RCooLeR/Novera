import { describe, expect, it } from "vitest";
import { buildIdentityText } from "./aboutBuildIdentity";

describe("buildIdentityText", () => {
  it("renders the executable identity without inventing missing values", () => {
    expect(
      buildIdentityText({
        productName: "Novera",
        version: "1.2.3",
        commit: "abc123",
        buildDate: "2026-07-17T10:00:00Z",
        channel: "stable",
        dirty: false,
        goVersion: "go1.26.4",
        wailsVersion: "v3.0.0-alpha.79",
      }),
    ).toBe("Novera 1.2.3 · stable · commit abc123 · built 2026-07-17T10:00:00Z");
  });

  it("marks locally modified builds", () => {
    expect(
      buildIdentityText({
        productName: "Novera",
        version: "0.1.0-dev",
        commit: "unknown",
        buildDate: "unknown",
        channel: "development",
        dirty: true,
        goVersion: "go1.26.4",
        wailsVersion: "v3.0.0-alpha.79",
      }),
    ).toContain("locally modified");
  });
});
