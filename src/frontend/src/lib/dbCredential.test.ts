import { describe, expect, it } from "vitest";
import type { DbProfile } from "./services";
import { dbCredentialPresentation } from "./dbCredential";

function profile(patch: Partial<DbProfile> = {}): DbProfile {
  return {
    id: "profile-a",
    name: "A",
    kind: "postgres",
    file: "",
    host: "db.example",
    port: 5432,
    database: "app",
    user: "reader",
    sslMode: "verify-full",
    secretRef: "db.cred.v1.opaque",
    password: "",
    ...patch,
  };
}

describe("database credential presentation", () => {
  it("labels only a backend-verified credential for the unchanged scope as stored", () => {
    const persisted = profile();
    expect(dbCredentialPresentation(profile(), persisted, "verified")).toEqual({
      label: "stored",
      placeholder: "Stored password — type to replace",
      verified: true,
    });
  });

  it.each([
    ["quarantined", "re-entry required"],
    ["unavailable", "unavailable"],
    [undefined, "unavailable"],
  ] as const)("fails closed for %s backend status", (status, label) => {
    expect(dbCredentialPresentation(profile(), profile(), status).label).toBe(label);
    expect(dbCredentialPresentation(profile(), profile(), status).verified).toBe(false);
  });

  it("requires re-entry after a destination-scope edit even if the old credential was verified", () => {
    const persisted = profile();
    const draft = profile({ host: "other.example" });
    expect(dbCredentialPresentation(draft, persisted, "verified")).toMatchObject({ label: "re-entry required", verified: false });
  });
});
