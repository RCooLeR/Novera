import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  open: vi.fn(),
  close: vi.fn(),
  listDir: vi.fn(),
  readFile: vi.fn(),
  writeFile: vi.fn(),
  rememberWorkspace: vi.fn(),
  settingsLoadError: vi.fn(),
  resetConversation: vi.fn(),
  beginWorkspaceTransition: vi.fn(),
  endWorkspaceTransition: vi.fn(),
  abortWorkspaceTransition: vi.fn(),
  agentStart: vi.fn(),
  agentCancel: vi.fn(),
  agentApprove: vi.fn(),
  agentApproveIntent: vi.fn(),
  llmSend: vi.fn(),
  llmCancel: vi.fn(),
  watch: vi.fn(),
  gitStatus: vi.fn(),
  diagnostics: vi.fn(),
  listArtifacts: vi.fn(),
	discardBigFileEdits: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: {
    Open: mocks.open,
    Close: mocks.close,
    ListDir: mocks.listDir,
    ReadFile: mocks.readFile,
    WriteFile: mocks.writeFile,
    Diagnostics: mocks.diagnostics,
  },
  Settings: { RememberWorkspace: mocks.rememberWorkspace, LoadError: mocks.settingsLoadError },
  Git: { Status: mocks.gitStatus },
  LLM: { Send: mocks.llmSend, Cancel: mocks.llmCancel },
  Agent: {
    ResetConversation: mocks.resetConversation,
    BeginWorkspaceTransition: mocks.beginWorkspaceTransition,
    EndWorkspaceTransition: mocks.endWorkspaceTransition,
    AbortWorkspaceTransition: mocks.abortWorkspaceTransition,
    Start: mocks.agentStart,
    Cancel: mocks.agentCancel,
    Approve: mocks.agentApprove,
    ApproveIntent: mocks.agentApproveIntent,
  },
  Db: {},
  Watcher: { Watch: mocks.watch },
  Jobs: {},
  Artifacts: { ListArtifacts: mocks.listArtifacts },
	BigFile: { DiscardEdits: mocks.discardBigFileEdits },
  SecretService: {},
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { useStore, type Tab } from "./store";

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

function dirtyTab(): Tab {
  return {
    instanceId: 9001,
    path: "notes.txt",
    name: "notes.txt",
    kind: "file",
    language: "plaintext",
    content: "unsaved A edit",
    savedContent: "A on disk",
    revision: "rev-a",
    binary: false,
    tooLarge: false,
    encoding: "utf-8",
  };
}

function cleanTab(): Tab {
  const tab = dirtyTab();
  return { ...tab, content: tab.savedContent };
}

function dirtyLargeFileTab(): Tab {
	return {
		...cleanTab(),
		instanceId: 9002,
		path: "huge.sql",
		name: "huge.sql",
		tooLarge: true,
		content: "",
		savedContent: "",
		largeFileSessionId: "f-large",
		largeFileDirty: true,
		largeFileInPlaceEligible: true,
	};
}

function installWorkspaceA(tab: Tab = cleanTab()) {
  useStore.setState({
    workspaceInstanceId: 100,
    workspaceTransitioning: false,
    root: "A",
    wsName: "A",
    isOpen: true,
    childrenByPath: { "": [] },
    expanded: { "": true },
    loadingPath: {},
    selectedPath: null,
    tabs: [tab],
    activePath: tab.path,
    status: null,
  });
}

afterEach(async () => {
  useStore.getState().cancelChat();
  useStore.getState().dismissStatus();
  useStore.setState({
    workspaceInstanceId: 0,
    workspaceTransitioning: false,
    root: "",
    wsName: "",
    isOpen: false,
    childrenByPath: {},
    expanded: {},
    loadingPath: {},
    selectedPath: null,
    tabs: [],
    activePath: null,
    status: null,
    chat: [],
    chatStreaming: false,
    streamReqId: null,
    streamMsgId: null,
    streamAttempt: 0,
    agentRunId: null,
    agentStarting: false,
    agentAttempt: 0,
    conversationResetting: false,
    conversationResetAttempt: 0,
    artifacts: [],
    artifactsError: null,
  });
  for (const mock of Object.values(mocks)) mock.mockReset();
});

function installSuccessfulDefaults() {
  mocks.close.mockResolvedValue(undefined);
  mocks.listDir.mockResolvedValue([]);
  mocks.rememberWorkspace.mockImplementation(async (root: string) => ({ recentWorkspaces: [root] }));
  mocks.settingsLoadError.mockResolvedValue("");
  mocks.resetConversation.mockResolvedValue(undefined);
  mocks.beginWorkspaceTransition.mockResolvedValue("transition-token");
  mocks.endWorkspaceTransition.mockResolvedValue(undefined);
  mocks.abortWorkspaceTransition.mockResolvedValue(undefined);
  mocks.agentCancel.mockResolvedValue(undefined);
  mocks.agentApprove.mockResolvedValue(undefined);
  mocks.agentApproveIntent.mockResolvedValue(undefined);
  mocks.llmCancel.mockResolvedValue(undefined);
  mocks.watch.mockResolvedValue(undefined);
  mocks.gitStatus.mockResolvedValue({ available: false, branch: "", head: "", aheadBehind: "", staged: [], unstaged: [], message: "" });
  mocks.diagnostics.mockResolvedValue({ diagnostics: [], scannedFiles: 0, truncated: false });
  mocks.listArtifacts.mockResolvedValue([]);
}

describe("workspace transition identity", () => {
  it("does not hold the Agent/root barrier while RememberWorkspace is pending", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const remember = deferred<{ recentWorkspaces: string[] }>();
    mocks.open.mockResolvedValue({ root: "B", name: "B" });
    mocks.rememberWorkspace.mockReturnValueOnce(remember.promise);

    await useStore.getState().openWorkspace("B");

    expect(mocks.endWorkspaceTransition).toHaveBeenCalledWith("transition-token");
    expect(useStore.getState()).toMatchObject({ root: "B", isOpen: true, workspaceTransitioning: false });
    expect(mocks.rememberWorkspace).toHaveBeenCalledWith("B");

    remember.resolve({ recentWorkspaces: ["B"] });
    await vi.waitFor(() => expect(useStore.getState().recents).toEqual(["B"]));
  });

  it("promotes a durable settings diagnostic when RememberWorkspace fails", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    mocks.open.mockResolvedValue({ root: "B", name: "B" });
    mocks.rememberWorkspace.mockRejectedValueOnce(new Error("save failed"));
    mocks.settingsLoadError.mockResolvedValueOnce("settings file is corrupt; mutations are blocked");

    await useStore.getState().openWorkspace("B");

    await vi.waitFor(() => {
      expect(useStore.getState().settingsError).toBe("settings file is corrupt; mutations are blocked");
    });
    expect(useStore.getState().status).toEqual({
      message: "settings file is corrupt; mutations are blocked",
      kind: "error",
    });
  });

  it("does not adopt an old cancellation event during a new Start attempt", () => {
    useStore.setState({ agentRunId: null, agentStarting: true, chatStreaming: true, chat: [] });

    useStore.getState().handleAgentEvent({ runId: "run-old", type: "error", text: "Run cancelled." });

    expect(useStore.getState()).toMatchObject({ agentRunId: null, agentStarting: true, chatStreaming: true, chat: [] });
  });

  it("cancels an Agent run id that resolves after the UI attempt was canceled", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const start = deferred<string>();
    mocks.agentStart.mockReturnValueOnce(start.promise);

    const sending = useStore.getState().sendAgent("inspect the workspace");
    await vi.waitFor(() => expect(mocks.agentStart).toHaveBeenCalledOnce());
    useStore.getState().cancelChat();
    start.resolve("run-late");
    await sending;

    expect(mocks.agentCancel).toHaveBeenCalledWith("run-late");
    expect(useStore.getState()).toMatchObject({ agentRunId: null, agentStarting: false, chatStreaming: false });
    const before = useStore.getState().chat;
    useStore.getState().handleAgentEvent({ runId: "run-late", seq: 1, type: "assistant_text", text: "stale" });
    expect(useStore.getState().chat).toEqual(before);
  });

  it("surfaces a user-triggered Agent cancellation rejection without restoring the active run", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ agentRunId: "run-cancel", chatStreaming: true });
    mocks.agentCancel.mockRejectedValueOnce(new Error("agent cancel transport failed"));

    useStore.getState().cancelChat();

    await vi.waitFor(() => expect(useStore.getState().status?.message).toBe("Agent cancellation failed: agent cancel transport failed"));
    expect(useStore.getState()).toMatchObject({ agentRunId: null, agentStarting: false, chatStreaming: false });
  });

  it("does not surface an old cancellation rejection in a successor workspace", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ agentRunId: "run-a", chatStreaming: true });
    const cancellation = deferred<void>();
    mocks.agentCancel.mockReturnValueOnce(cancellation.promise);

    useStore.getState().cancelChat();
    useStore.setState({
      workspaceInstanceId: 200,
      workspaceTransitioning: false,
      root: "B",
      wsName: "B",
      isOpen: true,
      status: { message: "B remains current", kind: "info" },
    });
    cancellation.reject(new Error("late A cancellation failure"));
    await cancellation.promise.catch(() => undefined);
    await Promise.resolve();

    expect(useStore.getState().status).toEqual({ message: "B remains current", kind: "info" });
  });

  it("buffers early out-of-order Agent events until Start confirms the run id", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const start = deferred<string>();
    mocks.agentStart.mockReturnValueOnce(start.promise);

    const sending = useStore.getState().sendAgent("review files");
    await vi.waitFor(() => expect(mocks.agentStart).toHaveBeenCalledOnce());
    useStore.getState().handleAgentEvent({ runId: "run-new", seq: 2, type: "done" });
    useStore.getState().handleAgentEvent({ runId: "run-new", seq: 1, type: "assistant_text", text: "ordered answer" });
    expect(useStore.getState().chat.filter((message) => message.role === "assistant")).toEqual([]);

    start.resolve("run-new");
    await sending;

    expect(useStore.getState().chat.some((message) => message.content === "ordered answer")).toBe(true);
    expect(useStore.getState()).toMatchObject({ agentRunId: null, agentStarting: false, chatStreaming: false });
  });

  it("fails an active Agent run safely when sequence metadata is missing", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    mocks.agentStart.mockResolvedValueOnce("run-unsequenced");
    await useStore.getState().sendAgent("review files");

    useStore.getState().handleAgentEvent({ runId: "run-unsequenced", type: "assistant_text", text: "unsafe order" });

    expect(mocks.agentCancel).toHaveBeenCalledWith("run-unsequenced");
    const chat = useStore.getState().chat;
    expect(chat[chat.length - 1]?.content).toContain("sequence number");
    expect(useStore.getState()).toMatchObject({ agentRunId: null, chatStreaming: false });
  });

  it("settles pending approval cards when a Jobs cancellation arrives as a terminal event", () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ agentRunId: "run-job", chatStreaming: true });

    useStore.getState().handleAgentEvent({
      runId: "run-job",
      seq: 1,
      type: "approval_request",
      callId: "run-job-tool-1",
      tool: "write_file",
    });
    useStore.getState().handleAgentEvent({ runId: "run-job", seq: 2, type: "assistant_text", text: "rolled back" });
    useStore.getState().handleAgentEvent({
      runId: "run-job",
      seq: 3,
      type: "error",
      text: "Run cancelled.",
      canceled: true,
      rolledBack: true,
    });

    expect(useStore.getState().chat.find((message) => message.callId === "run-job-tool-1")?.approval).toBe("denied");
    expect(useStore.getState().chat.some((message) => message.content === "rolled back")).toBe(false);
    expect(useStore.getState().chatStreaming).toBe(false);
  });

  it("returns the exact intent digest for tool approval and keeps checkpoints separate", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ agentRunId: "run-intent", chatStreaming: true });

    useStore.getState().handleAgentEvent({
      runId: "run-intent",
      seq: 1,
      type: "approval_request",
      callId: "run-intent-tool-1",
      tool: "write_file",
      args: `{"path":"a.txt","content":"complete-tail"}`,
      intent: `{"tool":"write_file","args":{"path":"a.txt","content":"complete-tail"}}`,
      intentDigest: "a".repeat(64),
      expiresAt: "2030-01-01T00:00:00Z",
    });

    const card = useStore.getState().chat.find((message) => message.callId === "run-intent-tool-1");
    expect(card).toMatchObject({ intentDigest: "a".repeat(64), approval: "pending" });
    await useStore.getState().approveAgent("run-intent-tool-1", true, card?.intentDigest);

    expect(mocks.agentApproveIntent).toHaveBeenCalledWith("run-intent-tool-1", "a".repeat(64), true);
    expect(mocks.agentApprove).not.toHaveBeenCalled();

    await useStore.getState().approveAgent("run-intent-continue-1", false);
    expect(mocks.agentApprove).toHaveBeenCalledWith("run-intent-continue-1", false);
  });

  it("removes run-scoped assistant output on an explicit UI cancellation", () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ agentRunId: "run-ui", chatStreaming: true });
    useStore.getState().handleAgentEvent({ runId: "run-ui", seq: 1, type: "assistant_text", text: "partial answer" });

    useStore.getState().cancelChat();

    expect(mocks.agentCancel).toHaveBeenCalledWith("run-ui");
    expect(useStore.getState().chat.some((message) => message.content === "partial answer")).toBe(false);
  });

  it("cancels an Ask request id that resolves after the UI attempt was canceled", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const send = deferred<string>();
    mocks.llmSend.mockReturnValueOnce(send.promise);

    const sending = useStore.getState().sendChat("explain this file");
    await vi.waitFor(() => expect(mocks.llmSend).toHaveBeenCalledOnce());
    useStore.getState().cancelChat();
    send.resolve("request-late");
    await sending;

    expect(mocks.llmCancel).toHaveBeenCalledWith("request-late");
    const before = useStore.getState().chat;
    useStore.getState().appendDelta("request-late", "stale", 1);
    expect(useStore.getState().chat).toEqual(before);
    expect(useStore.getState()).toMatchObject({ streamReqId: null, streamMsgId: null, chatStreaming: false });
  });

  it("surfaces a failed late Ask cancellation after the user canceled during Send", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const send = deferred<string>();
    mocks.llmSend.mockReturnValueOnce(send.promise);
    mocks.llmCancel.mockRejectedValueOnce(new Error("request cancel transport failed"));

    const sending = useStore.getState().sendChat("explain this file");
    await vi.waitFor(() => expect(mocks.llmSend).toHaveBeenCalledOnce());
    useStore.getState().cancelChat();
    send.resolve("request-late-failure");
    await sending;

    await vi.waitFor(() =>
      expect(useStore.getState().status?.message).toBe(
        "Assistant request cancellation failed: request cancel transport failed",
      ),
    );
    expect(useStore.getState()).toMatchObject({ streamReqId: null, streamMsgId: null, chatStreaming: false });
  });

  it("reorders early Ask events and drains the terminal only after prior deltas", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const send = deferred<string>();
    mocks.llmSend.mockReturnValueOnce(send.promise);

    const sending = useStore.getState().sendChat("explain this file");
    await vi.waitFor(() => expect(mocks.llmSend).toHaveBeenCalledOnce());
    useStore.getState().finishStream("request-new", 2);
    useStore.getState().appendDelta("request-new", "ordered delta", 1);
    send.resolve("request-new");
    await sending;

    const assistant = useStore.getState().chat.find((message) => message.role === "assistant");
    expect(assistant).toMatchObject({ content: "ordered delta", streaming: false });
    expect(useStore.getState()).toMatchObject({ streamReqId: null, streamMsgId: null, chatStreaming: false });
  });

  it("blocks both Assistant modes while a workspace transition is active", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ workspaceTransitioning: true });

    await useStore.getState().sendAgent("must not start");
    await useStore.getState().sendChat("must not send");

    expect(mocks.agentStart).not.toHaveBeenCalled();
    expect(mocks.llmSend).not.toHaveBeenCalled();
  });

  it("blocks a new send until Clear Chat's backend reset has completed", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const reset = deferred<void>();
    mocks.resetConversation.mockReturnValueOnce(reset.promise);

    useStore.getState().clearChat();
    await useStore.getState().sendAgent("must wait for reset");
    await useStore.getState().sendChat("must wait for reset");

    expect(useStore.getState().conversationResetting).toBe(true);
    expect(mocks.agentStart).not.toHaveBeenCalled();
    expect(mocks.llmSend).not.toHaveBeenCalled();

    reset.resolve();
    await vi.waitFor(() => expect(useStore.getState().conversationResetting).toBe(false));
  });

  it("surfaces artifact registry corruption diagnostics for the active workspace", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({
      artifacts: [
        {
          id: "stale-row",
          kind: "file",
          title: "Stale row",
          path: "stale.txt",
          tool: "test",
          note: "",
          sources: [],
          createdAt: 1,
          updatedAt: 1,
          archived: false,
          stale: false,
          missing: false,
        },
      ],
    });
    mocks.listArtifacts.mockRejectedValueOnce(new Error('artifact registry "A/.novera/artifacts.json" is corrupt; mutations are blocked'));

    await useStore.getState().loadArtifacts();

    expect(useStore.getState().artifacts).toEqual([]);
    expect(useStore.getState().artifactsError).toBe(
      'artifact registry "A/.novera/artifacts.json" is corrupt; mutations are blocked',
    );
    expect(useStore.getState().status).toEqual({
      message: 'artifact registry "A/.novera/artifacts.json" is corrupt; mutations are blocked',
      kind: "error",
    });
  });

  it("clears a prior artifact error only after an active-workspace load succeeds", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ artifactsError: "prior corruption", artifacts: [] });
    const recovered = {
      id: "recovered",
      kind: "file",
      title: "Recovered",
      path: "recovered.txt",
      tool: "test",
      note: "",
      sources: [],
      createdAt: 1,
      updatedAt: 1,
      archived: false,
      stale: false,
      missing: false,
    };
    mocks.listArtifacts.mockResolvedValueOnce([recovered]);

    await useStore.getState().loadArtifacts();

    expect(useStore.getState().artifacts).toEqual([recovered]);
    expect(useStore.getState().artifactsError).toBeNull();
  });

  it("orders delayed A -> B -> C intents and only commits the latest workspace", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const openB = deferred<{ root: string; name: string; isOpen: boolean }>();
    const openC = deferred<{ root: string; name: string; isOpen: boolean }>();
    mocks.open.mockReturnValueOnce(openB.promise).mockReturnValueOnce(openC.promise);

    const transitionB = useStore.getState().openWorkspace("B");
    await vi.waitFor(() => expect(mocks.open).toHaveBeenCalledWith("B"));
    const transitionC = useStore.getState().openWorkspace("C");

    expect(useStore.getState()).toMatchObject({ root: "A", workspaceTransitioning: true });
		expect(useStore.getState().tabs[0].content).toBe("A on disk");

    openB.resolve({ root: "B", name: "B", isOpen: true });
    await vi.waitFor(() => expect(mocks.open).toHaveBeenCalledTimes(2));
    expect(mocks.open).toHaveBeenNthCalledWith(2, "C");
    // B completed, but its stale response never replaced A's in-memory buffers.
    expect(useStore.getState().root).toBe("A");
		expect(useStore.getState().tabs[0].content).toBe("A on disk");

    openC.resolve({ root: "C", name: "C", isOpen: true });
    await Promise.all([transitionB, transitionC]);

    expect(useStore.getState()).toMatchObject({ root: "C", wsName: "C", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([]);
    expect(mocks.rememberWorkspace).toHaveBeenCalledTimes(1);
    expect(mocks.rememberWorkspace).toHaveBeenCalledWith("C");
  });

  it("clears renderer history when a newer intent supersedes an already-committed backend reset", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    useStore.setState({ chat: [{ id: "old-answer", role: "assistant", content: "A conversation" }] });
    const endB = deferred<void>();
    mocks.endWorkspaceTransition.mockReset();
    mocks.endWorkspaceTransition.mockReturnValueOnce(endB.promise).mockResolvedValue(undefined);
    mocks.open
      .mockResolvedValueOnce({ root: "B", name: "B", isOpen: true })
      .mockRejectedValueOnce(new Error("C is unavailable"))
      .mockResolvedValueOnce({ root: "A", name: "A", isOpen: true });

    const openingB = useStore.getState().openWorkspace("B");
    await vi.waitFor(() => expect(mocks.endWorkspaceTransition).toHaveBeenCalledOnce());
    const openingC = useStore.getState().openWorkspace("C");
    endB.resolve();

    await openingB;
    await expect(openingC).rejects.toThrow("C is unavailable");

    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().chat).toEqual([]);
  });

  it("preserves newer-owner chat when an older failed End settlement rejects", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const endB = deferred<void>();
    const openC = deferred<{ root: string; name: string; isOpen: boolean }>();
    mocks.endWorkspaceTransition.mockReset();
    mocks.endWorkspaceTransition.mockReturnValueOnce(endB.promise).mockResolvedValue(undefined);
    mocks.open
      .mockRejectedValueOnce(new Error("B is unavailable"))
      .mockRejectedValueOnce(new Error("A restore failed"))
      .mockReturnValueOnce(openC.promise);

    const openingB = useStore.getState().openWorkspace("B");
    void openingB.catch(() => undefined);
    await vi.waitFor(() => expect(mocks.endWorkspaceTransition).toHaveBeenCalledOnce());

    const openingC = useStore.getState().openWorkspace("C");
    await vi.waitFor(() => expect(mocks.open).toHaveBeenCalledWith("C"));
    useStore.setState({ chat: [{ id: "new-owner", role: "assistant", content: "C conversation" }] });

    // The rejected End never committed the old transition's reset, so its
    // stale recovery handler must not clear state now owned by C.
    endB.reject(new Error("barrier commit failed"));
    await openingB.catch(() => undefined);
    expect(useStore.getState()).toMatchObject({
      root: "A",
      isOpen: true,
      workspaceTransitioning: true,
      chat: [{ id: "new-owner", role: "assistant", content: "C conversation" }],
    });

    openC.resolve({ root: "C", name: "C", isOpen: true });
    await openingC;
    expect(useStore.getState()).toMatchObject({ root: "C", isOpen: true, workspaceTransitioning: false });
  });

  it("blocks a workspace switch before backend mutation while an editor is dirty", async () => {
    installSuccessfulDefaults();
    const tab = dirtyTab();
    installWorkspaceA(tab);

    await useStore.getState().openWorkspace("C");

    expect(mocks.open).not.toHaveBeenCalled();
    expect(mocks.beginWorkspaceTransition).not.toHaveBeenCalled();
    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([tab]);
    expect(useStore.getState().status?.message).toContain("unsaved editor and large-file changes");
  });

	it("blocks workspace close while an editor is dirty", () => {
		installSuccessfulDefaults();
		installWorkspaceA(dirtyTab());

		useStore.getState().closeWorkspace();

		expect(mocks.beginWorkspaceTransition).not.toHaveBeenCalled();
		expect(mocks.close).not.toHaveBeenCalled();
		expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
		expect(useStore.getState().status?.message).toContain("unsaved editor and large-file changes");
	});

	it("requires an explicit backend discard before closing a staged large-file tab", async () => {
		installSuccessfulDefaults();
		const tab = dirtyLargeFileTab();
		installWorkspaceA(tab);
		mocks.discardBigFileEdits.mockResolvedValue({ editCount: 0 });

		useStore.getState().requestCloseTab(tab.path);
		expect(useStore.getState().pendingTabClose).toBe(tab.path);
		expect(useStore.getState().tabs).toContainEqual(tab);

		await useStore.getState().confirmCloseTab(false);

		expect(mocks.discardBigFileEdits).toHaveBeenCalledWith("f-large");
		expect(useStore.getState().tabs).toEqual([]);
		expect(useStore.getState().pendingTabClose).toBeNull();
	});

  it("does not let an older deferred Abort cancel a newer Open intent", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const abortB = deferred<void>();
    const openC = deferred<{ root: string; name: string; isOpen: boolean }>();
    mocks.abortWorkspaceTransition.mockReset();
    mocks.abortWorkspaceTransition.mockReturnValueOnce(abortB.promise).mockResolvedValue(undefined);
    mocks.open
      .mockRejectedValueOnce(new Error("B is unavailable"))
      .mockResolvedValueOnce({ root: "A", name: "A", isOpen: true })
      .mockReturnValueOnce(openC.promise);

    const openingB = useStore.getState().openWorkspace("B");
    void openingB.catch(() => undefined);
    await vi.waitFor(() => expect(mocks.abortWorkspaceTransition).toHaveBeenCalledOnce());
    const openingC = useStore.getState().openWorkspace("C");
    await vi.waitFor(() => expect(mocks.open).toHaveBeenCalledWith("C"));

    abortB.resolve();
    await openingB.catch(() => undefined);
    expect(useStore.getState().workspaceTransitioning).toBe(true);

    openC.resolve({ root: "C", name: "C", isOpen: true });
    await openingC;
    expect(useStore.getState()).toMatchObject({ root: "C", isOpen: true, workspaceTransitioning: false });
  });

  it("awaits serialized watcher shutdown before opening a different root", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const watcherStop = deferred<void>();
    mocks.watch.mockReset();
    mocks.watch.mockReturnValueOnce(watcherStop.promise).mockResolvedValue(undefined);
    mocks.open.mockResolvedValueOnce({ root: "B", name: "B", isOpen: true });

    const opening = useStore.getState().openWorkspace("B");
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenCalledWith([]));
    expect(mocks.open).not.toHaveBeenCalled();

    watcherStop.resolve();
    await opening;

    expect(mocks.open).toHaveBeenCalledWith("B");
    expect(mocks.watch.mock.invocationCallOrder[0]).toBeLessThan(mocks.open.mock.invocationCallOrder[0]);
  });

  it("does not mutate the root when serialized watcher shutdown fails", async () => {
    installSuccessfulDefaults();
    const tab = cleanTab();
    installWorkspaceA(tab);
    mocks.watch.mockReset();
    mocks.watch.mockRejectedValueOnce(new Error("watcher stop failed")).mockResolvedValue(undefined);

    await expect(useStore.getState().openWorkspace("B")).rejects.toThrow("watcher stop failed");

    expect(mocks.open).not.toHaveBeenCalled();
    expect(mocks.abortWorkspaceTransition).toHaveBeenCalledWith("transition-token");
    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([tab]);
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["notes.txt"]));
  });

  it("blocks workspace Open when the Agent shutdown barrier times out", async () => {
    installSuccessfulDefaults();
    const tab = cleanTab();
    installWorkspaceA(tab);
    mocks.beginWorkspaceTransition.mockRejectedValueOnce(new Error("agent run did not stop; workspace transition blocked"));

    await expect(useStore.getState().openWorkspace("B")).rejects.toThrow("workspace transition blocked");

    expect(mocks.open).not.toHaveBeenCalled();
    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([tab]);
  });

  it("keeps the workspace attached when close cannot stop the Agent", async () => {
    installSuccessfulDefaults();
    const tab = cleanTab();
    installWorkspaceA(tab);
    mocks.beginWorkspaceTransition.mockRejectedValueOnce(new Error("agent run did not stop; workspace transition blocked"));

    useStore.getState().closeWorkspace();
    await vi.waitFor(() => expect(useStore.getState().status?.message).toContain("workspace transition blocked"));

    expect(mocks.close).not.toHaveBeenCalled();
    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([tab]);
  });

  it("awaits serialized watcher shutdown before closing the root", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const watcherStop = deferred<void>();
    mocks.watch.mockReset();
    mocks.watch.mockReturnValueOnce(watcherStop.promise);

    useStore.getState().closeWorkspace();
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenCalledWith([]));
    expect(mocks.close).not.toHaveBeenCalled();

    watcherStop.resolve();
    await vi.waitFor(() => expect(mocks.close).toHaveBeenCalledOnce());
    await vi.waitFor(() => expect(useStore.getState()).toMatchObject({ root: "", isOpen: false, workspaceTransitioning: false }));
    expect(mocks.watch.mock.invocationCallOrder[0]).toBeLessThan(mocks.close.mock.invocationCallOrder[0]);
  });

  it("keeps the root attached when watcher shutdown rejects during Close", async () => {
    installSuccessfulDefaults();
    const tab = cleanTab();
    installWorkspaceA(tab);
    mocks.watch.mockReset();
    mocks.watch.mockRejectedValueOnce(new Error("watcher stop failed")).mockResolvedValueOnce(undefined);

    useStore.getState().closeWorkspace();
    await vi.waitFor(() => expect(useStore.getState().status?.message).toBe("watcher stop failed"));

    expect(mocks.close).not.toHaveBeenCalled();
    expect(mocks.abortWorkspaceTransition).toHaveBeenCalledWith("transition-token");
    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([tab]);
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["notes.txt"]));
  });

  it("keeps the workspace attached and restores file watches when backend Close fails", async () => {
    installSuccessfulDefaults();
    const tab = cleanTab();
    installWorkspaceA(tab);
    mocks.close.mockRejectedValueOnce(new Error("close failed"));

    useStore.getState().closeWorkspace();
    await vi.waitFor(() => expect(useStore.getState().status?.message).toBe("close failed"));

    expect(useStore.getState()).toMatchObject({ root: "A", isOpen: true, workspaceTransitioning: false });
    expect(useStore.getState().tabs).toEqual([tab]);
    expect(mocks.watch).toHaveBeenLastCalledWith(["notes.txt"]);
    expect(mocks.abortWorkspaceTransition).toHaveBeenCalledWith("transition-token");
  });

  it("surfaces a watcher synchronization failure for the current workspace", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    mocks.watch.mockReset();
    mocks.watch.mockRejectedValueOnce(new Error("watch registration failed"));

    useStore.getState().syncWatches();

    await vi.waitFor(() =>
      expect(useStore.getState().status).toEqual({
        message: "File watcher update failed: watch registration failed",
        kind: "error",
      }),
    );
  });

  it("suppresses an old watcher failure after a successor workspace owns the UI", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const watchA = deferred<void>();
    mocks.watch.mockReset();
    mocks.watch.mockReturnValueOnce(watchA.promise).mockResolvedValueOnce(undefined);

    useStore.getState().syncWatches();
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenCalledWith(["notes.txt"]));
    useStore.setState({
      workspaceInstanceId: 300,
      workspaceTransitioning: false,
      root: "B",
      wsName: "B",
      isOpen: true,
      tabs: [],
      status: { message: "B remains current", kind: "info" },
    });
    useStore.getState().syncWatches();
    watchA.reject(new Error("late A watcher failure"));
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenCalledTimes(2));

    expect(useStore.getState().status).toEqual({ message: "B remains current", kind: "info" });
  });

  it("does not let a failed Close's deferred Abort cancel a newer Open intent", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const abortClose = deferred<void>();
    const openC = deferred<{ root: string; name: string; isOpen: boolean }>();
    mocks.close.mockRejectedValueOnce(new Error("close failed"));
    mocks.abortWorkspaceTransition.mockReset();
    mocks.abortWorkspaceTransition.mockReturnValueOnce(abortClose.promise).mockResolvedValue(undefined);
    mocks.open.mockReturnValueOnce(openC.promise);

    useStore.getState().closeWorkspace();
    await vi.waitFor(() => expect(mocks.abortWorkspaceTransition).toHaveBeenCalledOnce());
    const openingC = useStore.getState().openWorkspace("C");
    useStore.setState({ chat: [{ id: "new-owner", role: "assistant", content: "C conversation" }] });

    // The close operation still owns the serialized callback until Abort
    // settles. Its failure must not clear state owned by the newer Open or
    // mark that Open stale before it gets its turn in the queue.
    expect(mocks.open).not.toHaveBeenCalled();
    abortClose.reject(new Error("abort failed"));
    await vi.waitFor(() => expect(mocks.open).toHaveBeenCalledWith("C"));
    expect(useStore.getState()).toMatchObject({
      root: "A",
      isOpen: true,
      workspaceTransitioning: true,
      chat: [{ id: "new-owner", role: "assistant", content: "C conversation" }],
    });

    openC.resolve({ root: "C", name: "C", isOpen: true });
    await openingC;
    expect(useStore.getState()).toMatchObject({ root: "C", isOpen: true, workspaceTransitioning: false });
  });

  it("does not restore the old transcript when Close succeeded but barrier commit fails", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    mocks.endWorkspaceTransition.mockRejectedValue(new Error("barrier commit failed"));

    useStore.getState().closeWorkspace();
    await vi.waitFor(() => expect(useStore.getState().status?.message).toBe("barrier commit failed"));

    expect(mocks.close).toHaveBeenCalledOnce();
    expect(mocks.abortWorkspaceTransition).not.toHaveBeenCalled();
    expect(useStore.getState()).toMatchObject({ root: "", isOpen: false, workspaceTransitioning: false });
  });

  it("does not let an older delayed Close supersede a newer Open intent", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    mocks.open.mockResolvedValueOnce({ root: "B", name: "B", isOpen: true });

    useStore.getState().closeWorkspace();
    const openingB = useStore.getState().openWorkspace("B");
    await openingB;
    expect(useStore.getState()).toMatchObject({ root: "B", isOpen: true, workspaceTransitioning: false });

    await vi.waitFor(() => expect(mocks.beginWorkspaceTransition).toHaveBeenCalledTimes(1));

    expect(mocks.close).not.toHaveBeenCalled();
    expect(useStore.getState()).toMatchObject({ root: "B", isOpen: true, workspaceTransitioning: false });
  });

  it("drops delayed ListDir and ReadFile responses from A after B commits", async () => {
    installSuccessfulDefaults();
    installWorkspaceA();
    const oldList = deferred<Array<{ name: string; path: string; isDir: boolean; size: number; modTime: number }>>();
    const oldRead = deferred<{
      path: string;
      content: string;
      size: number;
      binary: boolean;
      tooLarge: boolean;
      revision: string;
      encoding: string;
    }>();
    mocks.listDir.mockImplementation((path: string) => (path === "src" ? oldList.promise : Promise.resolve([])));
    mocks.readFile.mockReturnValueOnce(oldRead.promise);
    mocks.open.mockResolvedValueOnce({ root: "B", name: "B", isOpen: true });

    const listingA = useStore.getState().loadChildren("src");
    const readingA = useStore.getState().openFile("old.txt", "old.txt");
    await useStore.getState().openWorkspace("B");

    oldList.resolve([{ name: "stale.ts", path: "src/stale.ts", isDir: false, size: 10, modTime: 1 }]);
    oldRead.resolve({
      path: "old.txt",
      content: "stale A content",
      size: 15,
      binary: false,
      tooLarge: false,
      revision: "rev-old",
      encoding: "utf-8",
    });
    await Promise.all([listingA, readingA]);

    expect(useStore.getState().root).toBe("B");
    expect(useStore.getState().childrenByPath.src).toBeUndefined();
    expect(useStore.getState().tabs).toEqual([]);
    expect(useStore.getState().status).toBeNull();
  });

  it("blocks a workspace transition until an in-flight dirty editor save succeeds", async () => {
    installSuccessfulDefaults();
    installWorkspaceA(dirtyTab());
    const writeA = deferred<{ path: string; revision: string }>();
    const openB = deferred<{ root: string; name: string; isOpen: boolean }>();
    mocks.writeFile.mockReturnValueOnce(writeA.promise);
    mocks.open.mockReturnValueOnce(openB.promise);

    const savingA = useStore.getState().saveTab("notes.txt");
    await useStore.getState().openWorkspace("B");
    expect(mocks.open).not.toHaveBeenCalled();
    expect(useStore.getState().root).toBe("A");

    writeA.resolve({ path: "notes.txt", revision: "rev-saved-a" });
		await expect(savingA).resolves.toMatchObject({ ok: true, outcome: "saved" });
		expect(useStore.getState().tabs[0]).toMatchObject({ savedContent: "unsaved A edit", revision: "rev-saved-a" });

		const transitionB = useStore.getState().openWorkspace("B");
    openB.resolve({ root: "B", name: "B", isOpen: true });
    await transitionB;
    expect(useStore.getState()).toMatchObject({ root: "B", workspaceTransitioning: false });
  });
});
