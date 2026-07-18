import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  writeFile: vi.fn(),
  readFile: vi.fn(),
  settingsSave: vi.fn(),
  settingsLoad: vi.fn(),
  settingsLoadError: vi.fn(),
  settingsGetLLMAPIKeyStatus: vi.fn(),
  settingsHasLLMAPIKey: vi.fn(),
  secretSetKey: vi.fn(),
  secretDeleteKey: vi.fn(),
  listModels: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: { WriteFile: mocks.writeFile, ReadFile: mocks.readFile },
  Settings: {
    Save: mocks.settingsSave,
    Load: mocks.settingsLoad,
    LoadError: mocks.settingsLoadError,
    GetLLMAPIKeyStatus: mocks.settingsGetLLMAPIKeyStatus,
    HasLLMAPIKey: mocks.settingsHasLLMAPIKey,
  },
  Git: {},
  LLM: { ListModels: mocks.listModels },
  Agent: {},
  Db: {},
  Watcher: { Watch: vi.fn().mockResolvedValue(undefined) },
  Jobs: {},
  Artifacts: {},
  SecretService: { SetKey: mocks.secretSetKey, DeleteKey: mocks.secretDeleteKey },
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { useStore, type Tab } from "./store";

const WORKSPACE_A = 101;
const WORKSPACE_B = 202;
let testTabInstanceId = 0;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function fileTab(overrides: Partial<Tab> = {}): Tab {
  return {
    instanceId: ++testTabInstanceId,
    path: "notes.txt",
    name: "notes.txt",
    kind: "file",
    language: "plaintext",
    content: "edited once",
    savedContent: "on disk",
    revision: "rev-1",
    binary: false,
    tooLarge: false,
    encoding: "utf-8",
    ...overrides,
  };
}

function settingsSnapshot(model: string) {
  return {
    theme: "dark",
    lastWorkspace: "",
    recentWorkspaces: [],
    uiFontSize: 13,
    editor: { fontSize: 13, tabSize: 4, wordWrap: false, minimap: true, theme: "novera-dark", formatOnSave: false },
    llm: {
      provider: "custom",
      baseURL: "https://provider.example/v1",
      model,
      apiKeyRef: "",
      requestTimeoutSec: 1800,
    },
    agent: {
      maxToolOutputChars: 6000,
      stepBatch: 50,
      maxTotalSteps: 1000,
      historyWindowGroups: 8,
      commandTimeoutSec: 60,
    },
  };
}

function installTab(tab = fileTab(), pendingTabClose: string | null = null, workspaceInstanceId = WORKSPACE_A) {
  useStore.setState({
    workspaceInstanceId,
    tabs: [tab],
    activePath: tab.path,
    pendingTabClose: null,
    pendingTabCloseAttemptToken: null,
    pendingTabCloseSaving: false,
    isOpen: false,
    status: null,
  });
  if (pendingTabClose) useStore.getState().requestCloseTab(pendingTabClose);
}

beforeEach(() => {
  mocks.readFile.mockRejectedValue(new Error("readFile is not configured for this test"));
  mocks.settingsLoadError.mockResolvedValue("");
  mocks.settingsGetLLMAPIKeyStatus.mockResolvedValue({ state: "missing", message: "" });
  mocks.settingsHasLLMAPIKey.mockResolvedValue(false);
  mocks.secretSetKey.mockResolvedValue(undefined);
  mocks.secretDeleteKey.mockResolvedValue(undefined);
});

afterEach(() => {
  useStore.getState().dismissStatus();
  useStore.setState({
    workspaceInstanceId: 0,
    tabs: [],
    activePath: null,
    pendingTabClose: null,
    pendingTabCloseAttemptToken: null,
    pendingTabCloseSaving: false,
    status: null,
    settings: null,
    settingsError: null,
    llmAPIKeyStatus: "missing",
    llmAPIKeyStatusMessage: "",
    llmAPIKeyAvailable: false,
    models: [],
  });
  mocks.writeFile.mockReset();
  mocks.readFile.mockReset();
  mocks.settingsSave.mockReset();
  mocks.settingsLoad.mockReset();
  mocks.settingsLoadError.mockReset();
  mocks.settingsGetLLMAPIKeyStatus.mockReset();
  mocks.settingsHasLLMAPIKey.mockReset();
  mocks.secretSetKey.mockReset();
  mocks.secretDeleteKey.mockReset();
  mocks.listModels.mockReset();
});

describe("settings saves", () => {
  it("deletes a stored API key explicitly and reports the authoritative missing state", async () => {
    const persisted = settingsSnapshot("model");
    useStore.setState({ settings: persisted, settingsError: null, llmAPIKeyAvailable: true, llmAPIKeyStatus: "verified" });
    mocks.settingsLoad.mockResolvedValue(persisted);
    mocks.settingsGetLLMAPIKeyStatus.mockResolvedValue({ state: "missing", message: "" });
    mocks.settingsHasLLMAPIKey.mockResolvedValue(false);

    await expect(useStore.getState().setApiKey("")).resolves.toBe(true);

    expect(mocks.secretDeleteKey).toHaveBeenCalledOnce();
    expect(mocks.secretSetKey).not.toHaveBeenCalled();
    expect(useStore.getState()).toMatchObject({
      llmAPIKeyAvailable: false,
      llmAPIKeyStatus: "missing",
      status: { message: "Stored API key removed", kind: "success" },
    });
  });

  it("returns failure and exposes unavailable credential storage without claiming the key is stored", async () => {
    const persisted = settingsSnapshot("model");
    useStore.setState({ settings: persisted, settingsError: null });
    mocks.secretSetKey.mockRejectedValueOnce(new Error("master key unavailable"));
    mocks.settingsLoad.mockResolvedValue(persisted);
    mocks.settingsGetLLMAPIKeyStatus.mockResolvedValueOnce({
      state: "unavailable",
      message: "Credential storage is unavailable: master key unavailable",
    });

    await expect(useStore.getState().setApiKey("typed-key")).resolves.toBe(false);

    expect(useStore.getState()).toMatchObject({
      llmAPIKeyStatus: "unavailable",
      llmAPIKeyStatusMessage: "Credential storage is unavailable: master key unavailable",
      llmAPIKeyAvailable: false,
    });
    expect(useStore.getState().status).toEqual({ message: "master key unavailable", kind: "error" });
  });

  it("restores backend-authoritative LLM settings after a rejected optimistic save", async () => {
    const persisted = settingsSnapshot("persisted-model");
    useStore.setState({ settings: persisted });
    mocks.settingsSave.mockRejectedValueOnce(new Error("settings file is corrupt; mutations are blocked"));
    mocks.settingsLoad.mockResolvedValue(persisted);

    await useStore.getState().saveLLMConfig({ model: "optimistic-model" });

    expect(mocks.settingsSave).toHaveBeenCalledWith(expect.objectContaining({ llm: expect.objectContaining({ model: "optimistic-model" }) }));
    expect(mocks.settingsLoad).toHaveBeenCalledTimes(2);
    expect(useStore.getState().settings?.llm.model).toBe("persisted-model");
    expect(useStore.getState().status).toEqual({ message: "settings file is corrupt; mutations are blocked", kind: "error" });
  });

  it("serializes independent whole-object saves and merges each patch into its backend predecessor", async () => {
    const initial = settingsSnapshot("initial-model");
    const afterLLM = { ...initial, llm: { ...initial.llm, model: "new-model" } };
    const final = { ...afterLLM, editor: { ...afterLLM.editor, tabSize: 7 } };
    const firstWrite = deferred<void>();
    useStore.setState({ settings: initial, settingsError: null });
    mocks.settingsLoad
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(afterLLM)
      .mockResolvedValueOnce(afterLLM)
      .mockResolvedValueOnce(final);
    mocks.settingsSave.mockReturnValueOnce(firstWrite.promise).mockResolvedValueOnce(undefined);

    const saveLLM = useStore.getState().saveLLMConfig({ model: "new-model" });
    const saveEditor = useStore.getState().saveEditorConfig({ tabSize: 7 });
    await vi.waitFor(() => expect(mocks.settingsSave).toHaveBeenCalledTimes(1));

    firstWrite.resolve();
    await Promise.all([saveLLM, saveEditor]);

    expect(mocks.settingsSave).toHaveBeenCalledTimes(2);
    expect(mocks.settingsSave).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        llm: expect.objectContaining({ model: "new-model" }),
        editor: expect.objectContaining({ tabSize: 7 }),
      }),
    );
    expect(useStore.getState().settings).toEqual(final);
  });
});

describe("model discovery", () => {
  it("discards a delayed result after the provider origin changes", async () => {
    const oldSettings = settingsSnapshot("");
    const currentSettings = {
      ...settingsSnapshot("current-model"),
      llm: {
        ...settingsSnapshot("current-model").llm,
        provider: "openai",
        baseURL: "https://new-provider.example/v1",
      },
    };
    const oldResult = deferred<string[]>();
    useStore.setState({ settings: oldSettings, settingsError: null, models: ["known-current-model"] });
    mocks.listModels.mockReturnValueOnce(oldResult.promise);

    const loading = useStore.getState().loadModels();
    useStore.setState({ settings: currentSettings, models: ["known-current-model"] });
    oldResult.resolve(["stale-old-provider-model"]);
    await loading;

    expect(useStore.getState().models).toEqual(["known-current-model"]);
    expect(mocks.settingsSave).not.toHaveBeenCalled();
  });

  it("lets only the latest discovery attempt commit for one provider", async () => {
    const settings = settingsSnapshot("selected-model");
    const olderResult = deferred<string[]>();
    useStore.setState({ settings, settingsError: null, models: [] });
    mocks.listModels.mockReturnValueOnce(olderResult.promise).mockResolvedValueOnce(["latest-model"]);

    const olderLoad = useStore.getState().loadModels();
    await useStore.getState().loadModels();
    olderResult.resolve(["stale-model"]);
    await olderLoad;

    expect(useStore.getState().models).toEqual(["latest-model"]);
  });
});

describe("tab saves", () => {
  it("retains the exact external revision when a dirty editor receives a watcher refresh", async () => {
    installTab();
    mocks.readFile.mockResolvedValueOnce({
      path: "notes.txt",
      content: "external content",
      revision: "external-rev",
      truncated: false,
      binary: false,
      tooLarge: false,
      size: 16,
      encoding: "utf-8",
    });

    await useStore.getState().reloadIfChanged("notes.txt");

    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "edited once",
      savedContent: "on disk",
      revision: "rev-1",
      staleOnDisk: true,
      staleRevision: "external-rev",
    });
  });

  it("overwrites only the exact external revision observed by the conflict UI", async () => {
    installTab(fileTab({ staleOnDisk: true, staleRevision: "external-rev" }));
    const write = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(write.promise);

    const saving = useStore.getState().overwriteStaleTab("notes.txt");
    useStore.getState().updateContent("notes.txt", "newer edit after confirmation");
    write.resolve({ path: "notes.txt", revision: "rev-overwritten" });

    await expect(saving).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "edited once" });
    expect(mocks.writeFile).toHaveBeenCalledWith("notes.txt", "edited once", "external-rev", "utf-8");
    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "newer edit after confirmation",
      savedContent: "edited once",
      revision: "rev-overwritten",
      staleOnDisk: false,
    });
    expect(useStore.getState().tabs[0].staleRevision).toBeUndefined();
  });

  it("refuses explicit overwrite when no exact external revision was observed", async () => {
    installTab(fileTab({ staleOnDisk: true, staleRevision: undefined }));

    await expect(useStore.getState().overwriteStaleTab("notes.txt")).resolves.toMatchObject({
      ok: false,
      outcome: "unavailable",
    });
    expect(mocks.writeFile).not.toHaveBeenCalled();
    expect(useStore.getState().tabs[0]).toMatchObject({ content: "edited once", savedContent: "on disk", staleOnDisk: true });
  });

  it("does not discard an edit made while a confirmed disk reload is in flight", async () => {
    installTab(fileTab({ staleOnDisk: true, staleRevision: "external-rev" }));
    const read = deferred<{
      path: string;
      content: string;
      revision: string;
      truncated: boolean;
      binary: boolean;
      tooLarge: boolean;
      size: number;
      encoding: string;
    }>();
    mocks.readFile.mockReturnValueOnce(read.promise);

    const reloading = useStore.getState().reloadTab("notes.txt");
    useStore.getState().updateContent("notes.txt", "typed during reload");
    read.resolve({
      path: "notes.txt",
      content: "new disk content",
      revision: "external-rev",
      truncated: false,
      binary: false,
      tooLarge: false,
      size: 16,
      encoding: "utf-8",
    });
    await reloading;

    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "typed during reload",
      savedContent: "on disk",
      staleOnDisk: true,
      staleRevision: "external-rev",
    });
    expect(useStore.getState().status).toEqual({
      message: "Reload was cancelled because the editor changed while the disk version was loading.",
      kind: "error",
    });
  });

  it("marks only the submitted snapshot as saved when the user edits during a write", async () => {
    installTab();
    const write = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(write.promise);

    const saving = useStore.getState().saveTab("notes.txt");
    useStore.getState().updateContent("notes.txt", "newer edit");
    write.resolve({ path: "notes.txt", revision: "rev-2" });

    await expect(saving).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "edited once" });
    expect(mocks.writeFile).toHaveBeenCalledWith("notes.txt", "edited once", "rev-1", "utf-8");
    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "newer edit",
      savedContent: "edited once",
      revision: "rev-2",
    });
  });

  it("keeps newer edits dirty after an encoding save", async () => {
    installTab(fileTab({ encoding: "utf-16le" }));
    const write = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(write.promise);

    const saving = useStore.getState().convertEncoding("notes.txt", "utf-8");
    useStore.getState().updateContent("notes.txt", "newer edit");
    write.resolve({ path: "notes.txt", revision: "rev-2" });

    await expect(saving).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "edited once" });
    expect(mocks.writeFile).toHaveBeenCalledWith("notes.txt", "edited once", "rev-1", "utf-8");
    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "newer edit",
      savedContent: "edited once",
      revision: "rev-2",
      encoding: "utf-8",
    });
  });

  it("serializes repeated saves and captures the latest content with the updated revision", async () => {
    installTab();
    const firstWrite = deferred<{ path: string; revision: string }>();
    const secondWrite = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(firstWrite.promise).mockReturnValueOnce(secondWrite.promise);

    const firstSave = useStore.getState().saveTab("notes.txt");
    useStore.getState().updateContent("notes.txt", "newer edit");
    const secondSave = useStore.getState().saveTab("notes.txt");
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);

    firstWrite.resolve({ path: "notes.txt", revision: "rev-2" });
    await expect(firstSave).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "edited once" });
    await vi.waitFor(() => expect(mocks.writeFile).toHaveBeenCalledTimes(2));
    expect(mocks.writeFile).toHaveBeenNthCalledWith(2, "notes.txt", "newer edit", "rev-2", "utf-8");

    secondWrite.resolve({ path: "notes.txt", revision: "rev-3" });
    await expect(secondSave).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "newer edit" });
    expect(useStore.getState().tabs[0]).toMatchObject({ content: "newer edit", savedContent: "newer edit", revision: "rev-3" });
  });

  it("shares the per-path queue between ordinary and encoding saves", async () => {
    installTab(fileTab({ encoding: "utf-16le" }));
    const firstWrite = deferred<{ path: string; revision: string }>();
    const encodingWrite = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(firstWrite.promise).mockReturnValueOnce(encodingWrite.promise);

    const firstSave = useStore.getState().saveTab("notes.txt");
    useStore.getState().updateContent("notes.txt", "latest text");
    const encodingSave = useStore.getState().convertEncoding("notes.txt", "utf-8");
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);

    firstWrite.resolve({ path: "notes.txt", revision: "rev-2" });
    await expect(firstSave).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "edited once" });
    await vi.waitFor(() => expect(mocks.writeFile).toHaveBeenCalledTimes(2));
    expect(mocks.writeFile).toHaveBeenNthCalledWith(2, "notes.txt", "latest text", "rev-2", "utf-8");

    encodingWrite.resolve({ path: "notes.txt", revision: "rev-3" });
    await expect(encodingSave).resolves.toMatchObject({ ok: true, outcome: "saved", submittedContent: "latest text" });
    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "latest text",
      savedContent: "latest text",
      revision: "rev-3",
      encoding: "utf-8",
    });
  });

  it("fails safely when an old save commits after the same path is closed and reopened", async () => {
    installTab();
    const oldWrite = deferred<{ path: string; revision: string }>();
    mocks.writeFile
      .mockReturnValueOnce(oldWrite.promise)
      .mockRejectedValueOnce(new Error("file changed on disk since it was last read"));

    const oldSave = useStore.getState().saveTab("notes.txt");
    useStore.getState().closeTab("notes.txt");
    installTab(fileTab({ content: "reopened edit" }));
    const reopenedSave = useStore.getState().saveTab("notes.txt");
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);

    oldWrite.resolve({ path: "notes.txt", revision: "rev-from-old-tab" });
    await expect(oldSave).resolves.toMatchObject({ ok: false, outcome: "superseded" });
    expect(useStore.getState().tabs[0]).toMatchObject({ content: "reopened edit", savedContent: "on disk", revision: "rev-1" });

    await vi.waitFor(() => expect(mocks.writeFile).toHaveBeenCalledTimes(2));
    expect(mocks.writeFile).toHaveBeenNthCalledWith(2, "notes.txt", "reopened edit", "rev-1", "utf-8");
    await expect(reopenedSave).resolves.toMatchObject({ ok: false, outcome: "failed", submittedContent: "reopened edit" });
    expect(useStore.getState().tabs[0]).toMatchObject({
      content: "reopened edit",
      savedContent: "on disk",
      revision: "rev-1",
    });
    expect(useStore.getState().status?.kind).toBe("error");
  });

  it("drops a queued operation when its tab instance is replaced at the same path", async () => {
    installTab();
    const firstWrite = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(firstWrite.promise);

    const firstSave = useStore.getState().saveTab("notes.txt");
    const queuedConversion = useStore.getState().convertEncoding("notes.txt", "latin-1");
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);

    const replacement = fileTab({ content: "replacement edit", savedContent: "replacement disk", revision: "rev-b" });
    installTab(replacement, null, WORKSPACE_A);
    firstWrite.resolve({ path: "notes.txt", revision: "rev-from-old-tab" });

    await expect(firstSave).resolves.toMatchObject({ ok: false, outcome: "superseded" });
    await expect(queuedConversion).resolves.toMatchObject({ ok: false, outcome: "superseded" });
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);
    expect(useStore.getState().tabs[0]).toEqual(replacement);
    expect(useStore.getState().status).toBeNull();
  });

  it("drops a queued operation when its workspace is replaced by a same-path tab", async () => {
    installTab();
    const firstWrite = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(firstWrite.promise);

    const firstSave = useStore.getState().saveTab("notes.txt");
    const queuedSave = useStore.getState().saveTab("notes.txt");
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);

    const replacement = fileTab({ content: "other workspace edit", savedContent: "other workspace disk", revision: "rev-other" });
    installTab(replacement, null, WORKSPACE_B);
    firstWrite.resolve({ path: "notes.txt", revision: "rev-from-workspace-a" });

    await expect(firstSave).resolves.toMatchObject({ ok: false, outcome: "superseded" });
    await expect(queuedSave).resolves.toMatchObject({ ok: false, outcome: "superseded" });
    expect(mocks.writeFile).toHaveBeenCalledTimes(1);
    expect(useStore.getState().workspaceInstanceId).toBe(WORKSPACE_B);
    expect(useStore.getState().tabs[0]).toEqual(replacement);
    expect(useStore.getState().status).toBeNull();
  });
});

describe("Save & Close", () => {
  it("closes only after the requested snapshot is confirmed clean", async () => {
    installTab(fileTab(), "notes.txt");
    mocks.writeFile.mockResolvedValueOnce({ path: "notes.txt", revision: "rev-2" });

    await useStore.getState().confirmCloseTab(true);

    expect(useStore.getState().tabs).toHaveLength(0);
    expect(useStore.getState().pendingTabClose).toBeNull();
  });

  it.each(["file changed on disk", "disk full"])("retains the dirty tab and dialog when saving fails: %s", async (message) => {
    installTab(fileTab(), "notes.txt");
    mocks.writeFile.mockRejectedValueOnce(new Error(message));

    await useStore.getState().confirmCloseTab(true);

    expect(useStore.getState().tabs).toHaveLength(1);
    expect(useStore.getState().tabs[0].savedContent).toBe("on disk");
    expect(useStore.getState().pendingTabClose).toBe("notes.txt");
    expect(useStore.getState().pendingTabCloseSaving).toBe(false);
    expect(useStore.getState().status?.kind).toBe("error");
  });

  it("retains the tab when a newer edit appears while Save & Close is in flight", async () => {
    installTab(fileTab(), "notes.txt");
    const write = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(write.promise);

    const closing = useStore.getState().confirmCloseTab(true);
    expect(useStore.getState().pendingTabCloseSaving).toBe(true);
    useStore.getState().updateContent("notes.txt", "newer edit");
    write.resolve({ path: "notes.txt", revision: "rev-2" });
    await closing;

    expect(useStore.getState().tabs[0]).toMatchObject({ content: "newer edit", savedContent: "edited once" });
    expect(useStore.getState().pendingTabClose).toBe("notes.txt");
    expect(useStore.getState().pendingTabCloseSaving).toBe(false);
  });

  it("does not let an old completion close a newly reopened dialog for the same path", async () => {
    installTab(fileTab(), "notes.txt");
    const write = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValueOnce(write.promise);

    const oldAttemptToken = useStore.getState().pendingTabCloseAttemptToken;
    const oldClosing = useStore.getState().confirmCloseTab(true);
    useStore.getState().cancelCloseTab();
    useStore.getState().requestCloseTab("notes.txt");
    const reopenedAttemptToken = useStore.getState().pendingTabCloseAttemptToken;
    expect(reopenedAttemptToken).not.toBe(oldAttemptToken);

    write.resolve({ path: "notes.txt", revision: "rev-2" });
    await oldClosing;

    expect(useStore.getState().tabs).toHaveLength(1);
    expect(useStore.getState().pendingTabClose).toBe("notes.txt");
    expect(useStore.getState().pendingTabCloseAttemptToken).toBe(reopenedAttemptToken);
    expect(useStore.getState().pendingTabCloseSaving).toBe(false);
  });
});
