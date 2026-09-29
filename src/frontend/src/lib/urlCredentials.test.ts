import { describe, expect, it } from "vitest";
import { stripUrlCredentials } from "./urlCredentials";

describe("stripUrlCredentials", () => {
  it("removes userinfo from a valid provider URL", () => {
    expect(stripUrlCredentials("https://user:secret@example.test/v1")).toBe("https://example.test/v1");
  });

  it("removes userinfo even when the remaining authority is malformed", () => {
    const sanitized = stripUrlCredentials("https://user:top-secret@[/v1");
    expect(sanitized).toBe("https://[/v1");
    expect(sanitized).not.toContain("top-secret");
  });

  it("does not rewrite non-URL text containing an at sign", () => {
    expect(stripUrlCredentials("local-user@example.test")).toBe("local-user@example.test");
  });
});
