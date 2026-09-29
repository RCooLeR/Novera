import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listJobs: vi.fn(),
  listArtifacts: vi.fn(),
  auditLog: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: {},
  Settings: {},
  Git: {},
  LLM: {},
  Agent: { AuditLog: mocks.auditLog },
  Db: {},
  Watcher: {},
  Jobs: { ListJobs: mocks.listJobs },
  Artifacts: { ListArtifacts: mocks.listArtifacts },
  BigFile: {},
  SecretService: {},
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import type { Artifact, AuditEntry, Job } from "../lib/services";
import { useStore } from "./store";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function installWorkspace(instanceId = 1) {
  useStore.setState({
    workspaceInstanceId: instanceId,
    workspaceTransitioning: false,
    root: `root-${instanceId}`,
    wsName: `workspace-${instanceId}`,
    isOpen: true,
    jobs: [],
    artifacts: [],
    artifactsError: null,
  });
}

function job(id: string, status = "running"): Job {
  return { id, kind: "test", title: id, status, startedAt: 1, endedAt: 0, error: "", log: [] } as Job;
}

function artifact(id: string): Artifact {
  return {
    id,
    kind: "file",
    title: id,
    path: `${id}.txt`,
    tool: "test",
    note: "",
    sources: [],
    createdAt: 1,
    updatedAt: 1,
    archived: false,
    stale: false,
    missing: false,
  } as Artifact;
}

function audit(summary: string): AuditEntry {
  return {
    time: "2030-01-01T00:00:00Z",
    runId: "run",
    tool: "read_file",
    summary,
    decision: "auto",
    status: "ok",
    detail: "",
  } as AuditEntry;
}

afterEach(async () => {
  useStore.getState().closeAuditLog();
  useStore.setState({
    workspaceInstanceId: 0,
    workspaceTransitioning: false,
    root: "",
    wsName: "",
    isOpen: false,
    jobs: [],
    artifacts: [],
    artifactsError: null,
    auditEntries: [],
    auditLoading: false,
    status: null,
  });
  await useStore.getState().loadJobs();
  await useStore.getState().loadArtifacts();
  for (const mock of Object.values(mocks)) mock.mockReset();
});

describe("async view snapshot ownership", () => {
  it("keeps the newest jobs response when bridge reads resolve out of order", async () => {
    installWorkspace();
    const oldRead = deferred<Job[]>();
    const newRead = deferred<Job[]>();
    mocks.listJobs.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise);

    const first = useStore.getState().loadJobs();
    const second = useStore.getState().loadJobs();
    newRead.resolve([job("new")]);
    await second;
    oldRead.resolve([job("old")]);
    await first;

    expect(useStore.getState().jobs.map((entry) => entry.id)).toEqual(["new"]);
  });

  it("does not commit a jobs response into a successor workspace", async () => {
    installWorkspace(1);
    const oldRead = deferred<Job[]>();
    mocks.listJobs.mockReturnValueOnce(oldRead.promise);
    const loading = useStore.getState().loadJobs();

    installWorkspace(2);
    oldRead.resolve([job("workspace-one")]);
    await loading;

    expect(useStore.getState().jobs).toEqual([]);
  });

  it("does not let an older artifact read replace a newer registry snapshot", async () => {
    installWorkspace();
    const oldRead = deferred<Artifact[]>();
    const newRead = deferred<Artifact[]>();
    mocks.listArtifacts.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise);

    const first = useStore.getState().loadArtifacts();
    const second = useStore.getState().loadArtifacts();
    newRead.resolve([artifact("new")]);
    await second;
    oldRead.resolve([artifact("old")]);
    await first;

    expect(useStore.getState().artifacts.map((entry) => entry.id)).toEqual(["new"]);
  });

  it("keeps a late audit refresh from overwriting the newest page", async () => {
    const oldRead = deferred<AuditEntry[]>();
    const newRead = deferred<AuditEntry[]>();
    mocks.auditLog.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise);

    const first = useStore.getState().openAuditLog();
    const second = useStore.getState().openAuditLog();
    newRead.resolve([audit("new")]);
    await second;
    oldRead.resolve([audit("old")]);
    await first;

    expect(useStore.getState().auditEntries.map((entry) => entry.summary)).toEqual(["new"]);
  });

  it("does not commit an audit response after the dialog is closed", async () => {
    const read = deferred<AuditEntry[]>();
    mocks.auditLog.mockReturnValueOnce(read.promise);
    const loading = useStore.getState().openAuditLog();
    useStore.getState().closeAuditLog();
    read.resolve([audit("late")]);
    await loading;

    expect(useStore.getState()).toMatchObject({ auditOpen: false, auditLoading: false, auditEntries: [] });
  });
});
