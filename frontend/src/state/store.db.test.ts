import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  credentialStatuses: vi.fn(),
  loadError: vi.fn(),
  saveProfileReconciled: vi.fn(),
  deleteProfileReconciled: vi.fn(),
  listTables: vi.fn(),
  buildTableQuery: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: {},
  Settings: {},
  Git: {},
  LLM: {},
  Agent: {},
  Db: {
    ListProfiles: mocks.listProfiles,
    CredentialStatuses: mocks.credentialStatuses,
    LoadError: mocks.loadError,
    SaveProfileReconciled: mocks.saveProfileReconciled,
    DeleteProfileReconciled: mocks.deleteProfileReconciled,
    ListTables: mocks.listTables,
    BuildTableQuery: mocks.buildTableQuery,
  },
  Watcher: { Watch: vi.fn().mockResolvedValue(undefined) },
  Jobs: {},
  Artifacts: {},
  SecretService: {},
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import type { DbProfile, DbTable } from "../lib/services";
import { useStore } from "./store";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

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

beforeEach(() => {
  mocks.listProfiles.mockReset();
  mocks.credentialStatuses.mockReset();
  mocks.loadError.mockReset();
  mocks.saveProfileReconciled.mockReset();
  mocks.deleteProfileReconciled.mockReset();
  mocks.listTables.mockReset();
  mocks.buildTableQuery.mockReset();
  useStore.setState({
    dbProfiles: [],
    dbCredentialStatuses: {},
    dbError: null,
    activeDbId: null,
    dbTables: [],
    dbTablesLoading: false,
    dbSql: {},
    tabs: [],
    activePath: null,
    status: null,
  });
});

afterEach(() => {
  useStore.getState().dismissStatus();
});

describe("database mutation reconciliation", () => {
  it("does not apply a credential status from a different profile-scope snapshot", async () => {
    const persisted = profile();
    mocks.listProfiles.mockResolvedValue([persisted]);
    mocks.loadError.mockResolvedValue("");
    mocks.credentialStatuses.mockResolvedValue([
      { profileId: persisted.id, secretRef: "db.cred.v1.changed-scope", status: "verified" },
    ]);

    await useStore.getState().loadDbProfiles();

    expect(useStore.getState().dbProfiles).toEqual([persisted]);
    expect(useStore.getState().dbCredentialStatuses).toEqual({});
  });

  it("deduplicates a double-submitted empty-id draft and retains the published backend identity", async () => {
    const draft = profile({ id: "", secretRef: "", password: "new-password" });
    const saved = profile({ id: "backend-issued-id" });
    const warning = "Saved connections were published but could not be safely finalized.";
    const pending = deferred<{ profile: DbProfile; finalizationWarning: string }>();
    mocks.saveProfileReconciled.mockReturnValueOnce(pending.promise);
    mocks.listProfiles.mockResolvedValue([saved]);
    mocks.loadError.mockResolvedValue(warning);
    mocks.credentialStatuses.mockResolvedValue([{ profileId: saved.id, secretRef: saved.secretRef, status: "verified" }]);

    const first = useStore.getState().saveDbProfile(draft);
    const second = useStore.getState().saveDbProfile(draft);
    await vi.waitFor(() => expect(mocks.saveProfileReconciled).toHaveBeenCalledTimes(1));
    pending.resolve({ profile: saved, finalizationWarning: warning });

    await expect(Promise.all([first, second])).resolves.toEqual([saved, saved]);
    expect(mocks.saveProfileReconciled).toHaveBeenCalledTimes(1);
    expect(useStore.getState().dbProfiles).toEqual([saved]);
    expect(useStore.getState().dbCredentialStatuses).toEqual({ [saved.id]: "verified" });
    expect(useStore.getState().dbError).toBe(warning);
    expect(useStore.getState().status).toEqual({ message: warning, kind: "error" });
  });

  it("removes renderer state after a published delete while retaining the finalization warning", async () => {
    const saved = profile();
    const warning = "Deletion is visible but finalization is uncertain.";
    useStore.setState({ dbProfiles: [saved], activeDbId: saved.id, dbSql: { [saved.id]: "SELECT 1" } });
    mocks.deleteProfileReconciled.mockResolvedValue({ deleted: true, finalizationWarning: warning });
    mocks.listProfiles.mockResolvedValue([]);
    mocks.loadError.mockResolvedValue(warning);
    mocks.credentialStatuses.mockResolvedValue([]);

    await useStore.getState().deleteDbProfile(saved.id);

    expect(mocks.deleteProfileReconciled).toHaveBeenCalledTimes(1);
    expect(useStore.getState()).toMatchObject({ dbProfiles: [], activeDbId: null, dbSql: {}, dbError: warning });
    expect(useStore.getState().status).toEqual({ message: warning, kind: "error" });
  });

  it("reloads authoritative state after a rejected pre-publication save", async () => {
    const existing = profile();
    useStore.setState({ dbProfiles: [] });
    mocks.saveProfileReconciled.mockRejectedValue(new Error("disk is read-only"));
    mocks.listProfiles.mockResolvedValue([existing]);
    mocks.loadError.mockResolvedValue("");
    mocks.credentialStatuses.mockResolvedValue([{ profileId: existing.id, secretRef: existing.secretRef, status: "unavailable" }]);

    await expect(useStore.getState().saveDbProfile(profile({ id: "" }))).resolves.toBeNull();

    expect(useStore.getState().dbProfiles).toEqual([existing]);
    expect(useStore.getState().dbCredentialStatuses).toEqual({ [existing.id]: "unavailable" });
    expect(useStore.getState().status).toEqual({ message: "disk is read-only", kind: "error" });
  });
});

describe("database table previews", () => {
  const adversarialTable: DbTable = {
    schema: `tenant"east`,
    name: `orders"; DROP TABLE audit; --`,
    type: "table",
  };

  it("passes structured identity to the backend and uses only backend-built SQL", async () => {
    const saved = profile();
    const backendSQL = `SELECT * FROM "tenant""east"."orders""; DROP TABLE audit; --" LIMIT 100`;
    useStore.setState({ dbProfiles: [saved], activeDbId: saved.id });
    mocks.buildTableQuery.mockResolvedValue(backendSQL);

    await useStore.getState().openDbTable(saved.id, adversarialTable);

    expect(mocks.buildTableQuery).toHaveBeenCalledWith(saved.id, adversarialTable.schema, adversarialTable.name, 100);
    expect(useStore.getState().dbSql[saved.id]).toBe(backendSQL);
    expect(useStore.getState().activePath).toBe(`db:${saved.id}`);
  });

  it("drops a delayed preview after the connection changes away and back", async () => {
    const a = profile();
    const b = profile({ id: "profile-b", name: "B" });
    const pending = deferred<string>();
    useStore.setState({ dbProfiles: [a, b], activeDbId: a.id });
    mocks.buildTableQuery.mockReturnValue(pending.promise);
    mocks.listTables.mockResolvedValue([]);

    const opening = useStore.getState().openDbTable(a.id, adversarialTable);
    await vi.waitFor(() => expect(mocks.buildTableQuery).toHaveBeenCalledOnce());
    await useStore.getState().selectDb(b.id);
    await useStore.getState().selectDb(a.id);
    pending.resolve("stale backend SQL");
    await opening;

    expect(useStore.getState().activeDbId).toBe(a.id);
    expect(useStore.getState().dbSql[a.id]).toBeUndefined();
    expect(useStore.getState().activePath).toBeNull();
  });
});
