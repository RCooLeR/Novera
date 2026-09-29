import type { DbProfile } from "./services";

export type DbCredentialStatus = "none" | "verified" | "quarantined" | "unavailable";

export interface DbCredentialPresentation {
  label: string | null;
  placeholder: string;
  verified: boolean;
}

function normalizedPort(profile: DbProfile): number {
  if (profile.port) return profile.port;
  if (profile.kind.trim().toLowerCase() === "postgres") return 5432;
  if (profile.kind.trim().toLowerCase() === "mysql") return 3306;
  return 0;
}

// Match the backend's credential-scope normalization. A false negative merely
// asks for a safe re-entry; a false positive could incorrectly claim that a
// password is usable for an edited destination, so unknown/missing inputs fail
// closed.
export function sameDbCredentialScope(draft: DbProfile, persisted: DbProfile | undefined): boolean {
  if (!persisted || !draft.id || draft.id !== persisted.id) return false;
  const normalize = (value: string) => value.trim();
  return (
    draft.kind.trim().toLowerCase() === persisted.kind.trim().toLowerCase() &&
    (normalize(draft.host) || "localhost").toLowerCase() === (normalize(persisted.host) || "localhost").toLowerCase() &&
    normalizedPort(draft) === normalizedPort(persisted) &&
    normalize(draft.database) === normalize(persisted.database) &&
    normalize(draft.user) === normalize(persisted.user) &&
    normalize(draft.sslMode).toLowerCase() === normalize(persisted.sslMode).toLowerCase()
  );
}

// A SecretRef is never enough to display "stored". Only a backend-verified,
// readable credential for the unchanged destination scope earns that label.
export function dbCredentialPresentation(
  draft: DbProfile,
  persisted: DbProfile | undefined,
  status: DbCredentialStatus | undefined,
): DbCredentialPresentation {
  if (!draft.secretRef) return { label: null, placeholder: "", verified: false };
  if (!sameDbCredentialScope(draft, persisted)) {
    return { label: "re-entry required", placeholder: "Password must be re-entered for the changed destination", verified: false };
  }
  if (status === "verified") {
    return { label: "stored", placeholder: "Stored password — type to replace", verified: true };
  }
  if (status === "quarantined") {
    return { label: "re-entry required", placeholder: "Legacy password is quarantined — type to re-enter", verified: false };
  }
  return { label: "unavailable", placeholder: "Saved password is unavailable — type to replace", verified: false };
}
