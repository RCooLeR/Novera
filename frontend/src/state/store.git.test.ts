import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  stage: vi.fn(),
  unstage: vi.fn(),
  stageAll: vi.fn(),
  commit: vi.fn(),
  diff: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: {},
  Settings: {},
  Git: {
    Status: mocks.status,
    Stage: mocks.stage,
    Unstage: mocks.unstage,
    StageAll: mocks.stageAll,
    Commit: mocks.commit,
    Diff: mocks.diff,
  },
  LLM: {},
  Agent: {},
  Db: {},
  Watcher: {},
  Jobs: {},
  Artifacts: {},
  SecretService: {},
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import type { GitStatus } from "../lib/services";
import { useStore } from "./store";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function gitStatus(head: string): GitStatus {
  return {
    available: true,
    branch: "main",
    head,
    aheadBehind: "",
    staged: [],
    unstaged: [],
    message: "",
  } as GitStatus;
}

function installWorkspace(workspaceInstanceId = 501, status = gitStatus("initial")) {
  useStore.setState({
    workspaceInstanceId,
    workspaceTransitioning: false,
    root: `workspace-${workspaceInstanceId}`,
    wsName: `workspace-${workspaceInstanceId}`,
    isOpen: true,
    gitStatus: status,
    gitBusy: false,
    status: null,
  });
}

afterEach(() => {
  useStore.getState().dismissStatus();
  useStore.setState({
    workspaceInstanceId: 0,
    workspaceTransitioning: false,
    root: "",
    wsName: "",
    isOpen: false,
    gitStatus: null,
    gitBusy: false,
    status: null,
  });
  for (const mock of Object.values(mocks)) mock.mockReset();
});

describe("serialized Git operations", () => {
  it("finishes an older status read before dispatching a newer stage and commits the authoritative post-stage refresh", async () => {
    installWorkspace();
    const oldRead = deferred<GitStatus>();
    const stageBridge = deferred<GitStatus>();
    const refreshed = deferred<GitStatus>();
    mocks.status.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(refreshed.promise);
    mocks.stage.mockReturnValueOnce(stageBridge.promise);

    const loading = useStore.getState().loadGitStatus();
    const staging = useStore.getState().stageFile("notes.txt");

    expect(useStore.getState().gitBusy).toBe(true);
    expect(mocks.stage).not.toHaveBeenCalled();

    oldRead.resolve(gitStatus("old-read"));
    await vi.waitFor(() => expect(mocks.stage).toHaveBeenCalledWith("notes.txt"));

    stageBridge.resolve(gitStatus("stage-return"));
    await vi.waitFor(() => expect(mocks.status).toHaveBeenCalledTimes(2));
    refreshed.resolve(gitStatus("authoritative-after-stage"));
    await Promise.all([loading, staging]);

    expect(useStore.getState().gitStatus?.head).toBe("authoritative-after-stage");
    expect(useStore.getState().gitBusy).toBe(false);
  });

  it("keeps commit and a store-level stage caller in one busy queue without dropping the later mutation", async () => {
    installWorkspace();
    const commitBridge = deferred<{
      hash: string;
      shortHash: string;
      subject: string;
      message: string;
      status: GitStatus;
      committed: boolean;
      refreshError: string;
    }>();
    const stageBridge = deferred<GitStatus>();
    mocks.commit.mockReturnValueOnce(commitBridge.promise);
    mocks.status.mockResolvedValueOnce(gitStatus("after-commit")).mockResolvedValueOnce(gitStatus("after-stage"));
    mocks.stage.mockReturnValueOnce(stageBridge.promise);

    const committing = useStore.getState().commit("subject", "body");
    const staging = useStore.getState().stageFile("later.txt");

    expect(useStore.getState().gitBusy).toBe(true);
    expect(mocks.stage).not.toHaveBeenCalled();

    commitBridge.resolve({
      hash: "abc123",
      shortHash: "abc123",
      subject: "subject",
      message: "",
      status: gitStatus("commit-return"),
      committed: true,
      refreshError: "",
    });
    await vi.waitFor(() => expect(mocks.stage).toHaveBeenCalledWith("later.txt"));
    expect(useStore.getState().gitBusy).toBe(true);

    stageBridge.resolve(gitStatus("stage-return"));
    await expect(committing).resolves.toBe(true);
    await staging;

    expect(mocks.status).toHaveBeenCalledTimes(2);
    expect(useStore.getState().gitStatus?.head).toBe("after-stage");
    expect(useStore.getState().gitBusy).toBe(false);
  });

  it("drops an in-flight read and its queued mutation after the workspace identity changes", async () => {
    installWorkspace(601, gitStatus("workspace-a"));
    const oldRead = deferred<GitStatus>();
    mocks.status.mockReturnValueOnce(oldRead.promise);

    const loading = useStore.getState().loadGitStatus();
    const staging = useStore.getState().stageFile("a-only.txt");
    installWorkspace(602, gitStatus("workspace-b"));

    oldRead.resolve(gitStatus("late-workspace-a"));
    await Promise.all([loading, staging]);

    expect(mocks.stage).not.toHaveBeenCalled();
    expect(useStore.getState().gitStatus?.head).toBe("workspace-b");
    expect(useStore.getState().gitBusy).toBe(false);
  });

  it("reconciles status after an ambiguous mutation rejection", async () => {
    installWorkspace();
    mocks.stage.mockRejectedValueOnce(new Error("index is locked"));
    mocks.status.mockResolvedValueOnce(gitStatus("authoritative-after-error"));

    await useStore.getState().stageFile("notes.txt");

    expect(mocks.status).toHaveBeenCalledTimes(1);
    expect(useStore.getState().gitStatus?.head).toBe("authoritative-after-error");
    expect(useStore.getState().status).toEqual({ message: "Stage failed: index is locked", kind: "error" });
    expect(useStore.getState().gitBusy).toBe(false);
  });

  it("uses the mutation-returned snapshot but surfaces an authoritative refresh failure", async () => {
    installWorkspace();
    mocks.unstage.mockResolvedValueOnce(gitStatus("unstage-return"));
    mocks.status.mockRejectedValueOnce(new Error("status unavailable"));

    await useStore.getState().unstageFile("notes.txt");

    expect(useStore.getState().gitStatus?.head).toBe("unstage-return");
    expect(useStore.getState().status).toEqual({
      message: "Git status refresh failed: status unavailable",
      kind: "error",
    });
    expect(useStore.getState().gitBusy).toBe(false);
  });
});
