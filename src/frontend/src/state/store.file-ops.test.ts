import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  rename: vi.fn(),
  remove: vi.fn(),
  createFile: vi.fn(),
  createDir: vi.fn(),
  listDir: vi.fn(),
  readFile: vi.fn(),
  writeFile: vi.fn(),
  search: vi.fn(),
  diagnostics: vi.fn(),
  listAllFiles: vi.fn(),
  gitStatus: vi.fn(),
  gitDiff: vi.fn(),
  watch: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: {
    Rename: mocks.rename,
    Delete: mocks.remove,
    CreateFile: mocks.createFile,
    CreateDir: mocks.createDir,
    ListDir: mocks.listDir,
    ReadFile: mocks.readFile,
    WriteFile: mocks.writeFile,
    Search: mocks.search,
    Diagnostics: mocks.diagnostics,
    ListAllFiles: mocks.listAllFiles,
  },
  Settings: {},
  Git: { Status: mocks.gitStatus, Diff: mocks.gitDiff },
  LLM: {},
  Agent: {},
  Db: {},
  Watcher: { Watch: mocks.watch },
  Jobs: {},
  Artifacts: {},
  SecretService: {},
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import type { Entry, GitStatus } from "../lib/services";
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

let tabId = 1000;

function fileTab(path: string, content = "saved", savedContent = content): Tab {
  return {
    instanceId: ++tabId,
    path,
    name: path.split("/").pop() ?? path,
    kind: "file",
    language: "plaintext",
    content,
    savedContent,
    revision: `rev:${path}`,
    binary: false,
    tooLarge: false,
    encoding: "utf-8",
  };
}

function tableTab(path: string): Tab {
  return {
    instanceId: ++tabId,
    path,
    name: path.split("/").pop() ?? path,
    kind: "table",
    language: "",
    content: "",
    savedContent: "",
    revision: "",
    binary: false,
    tooLarge: false,
    rel: path,
  };
}

function diffTab(path: string, staged: boolean): Tab {
  return {
    instanceId: ++tabId,
    path: `diff:${staged ? "s" : "u"}:${path}`,
    name: `${path.split("/").pop()} ${staged ? "(staged)" : "(working tree)"}`,
    kind: "diff",
    language: "plaintext",
    content: "",
    savedContent: "",
    revision: "",
    binary: false,
    tooLarge: false,
    rel: path,
    diffOld: "before",
    diffNew: "after",
  };
}

function entry(path: string, isDir = false): Entry {
  return {
    path,
    name: path.split("/").pop() ?? path,
    isDir,
    size: 1,
    modTime: 1,
  } as Entry;
}

function gitStatus(paths: string[] = []): GitStatus {
  return {
    available: true,
    branch: "main",
    head: "abc",
    aheadBehind: "",
    staged: paths.map((path) => ({ path, oldPath: "", index: "M", worktree: " ", summary: "modified", staged: true })),
    unstaged: paths.map((path) => ({ path, oldPath: "", index: " ", worktree: "M", summary: "modified", staged: false })),
    message: "",
  } as GitStatus;
}

function installWorkspace(tabs: Tab[], workspaceInstanceId = 700) {
  useStore.setState({
    workspaceInstanceId,
    workspaceTransitioning: false,
    root: `root-${workspaceInstanceId}`,
    wsName: `workspace-${workspaceInstanceId}`,
    isOpen: true,
    childrenByPath: { "": [] },
    expanded: { "": true },
    loadingPath: {},
    selectedPath: null,
    tabs,
    activePath: tabs[0]?.path ?? null,
    gitStatus: gitStatus(),
    gitBusy: false,
    fileMenu: null,
    pendingFileOp: null,
    pendingDelete: null,
    searchQuery: "",
    searchResults: null,
    searching: false,
    pendingReveal: null,
    pendingTabClose: null,
    pendingTabCloseAttemptToken: null,
    pendingTabCloseSaving: false,
    toolResult: null,
    toolBusy: false,
    cleanDump: null,
    dataTools: null,
    allFiles: [],
    loadingAllFiles: false,
    allFilesError: null,
    diagnostics: null,
    loadingDiagnostics: false,
    status: null,
  });
}

beforeEach(() => {
  mocks.rename.mockResolvedValue(undefined);
  mocks.remove.mockResolvedValue(undefined);
  mocks.createFile.mockResolvedValue(entry("new.txt"));
  mocks.createDir.mockResolvedValue(entry("new", true));
  mocks.listDir.mockResolvedValue([]);
  mocks.writeFile.mockResolvedValue({ path: "", revision: "saved-revision" });
  mocks.gitStatus.mockResolvedValue(gitStatus());
  mocks.watch.mockResolvedValue(undefined);
});

afterEach(async () => {
  await Promise.resolve();
  useStore.getState().dismissStatus();
  installWorkspace([], 0);
  useStore.setState({ root: "", wsName: "", isOpen: false, expanded: {}, childrenByPath: {}, gitStatus: null });
  for (const mock of Object.values(mocks)) mock.mockReset();
});

describe("resource-aware explorer rename reconciliation", () => {
  it("renames a dirty file and both diff identities without losing content, then watches the new path", async () => {
    const dirty = fileTab("src/old.txt", "unsaved edit", "saved base");
    const unstaged = diffTab("src/old.txt", false);
    const staged = diffTab("src/old.txt", true);
    installWorkspace([dirty, unstaged, staged]);
    const statusRefresh = deferred<GitStatus>();
    mocks.gitStatus.mockReset();
    mocks.gitStatus.mockReturnValue(statusRefresh.promise);
    mocks.listDir.mockResolvedValue([entry("src/new.md")]);
    useStore.setState({
      childrenByPath: { "": [entry("src", true)], src: [entry("src/old.txt")] },
      expanded: { "": true, src: true },
      selectedPath: "src/old.txt",
      activePath: staged.path,
      pendingTabClose: "src/old.txt",
      pendingTabCloseAttemptToken: 91,
      pendingReveal: { path: "src/old.txt", line: 4, column: 2 },
      searchResults: {
        matches: [{ path: "src/old.txt", line: 4, column: 2, text: "hit" }],
        fileCount: 1,
        truncated: false,
      },
      diagnostics: {
        items: [{ path: "src/old.txt", line: 4, column: 2, severity: "warning", kind: "TODO", message: "todo" }],
        fileCount: 1,
        truncated: false,
      },
      allFiles: ["src/old.txt"],
      gitStatus: gitStatus(["src/old.txt"]),
      cleanDump: { rel: "src/old.txt" },
      dataTools: { rel: "src/old.txt" },
      toolResult: { kind: "sql", title: "preview", rel: "src/old.txt", table: "t", out: "src/old.sql", sql: "SQL" },
      pendingFileOp: { type: "rename", targetRel: "src/old.txt", initial: "old.txt", title: "Rename" },
    });

    await useStore.getState().submitFileOp("new.md");

    expect(mocks.rename).toHaveBeenCalledWith("src/old.txt", "src/new.md");
    expect(useStore.getState().tabs).toEqual([
      expect.objectContaining({
        path: "src/new.md",
        name: "new.md",
        language: "markdown",
        content: "unsaved edit",
        savedContent: "saved base",
        revision: "rev:src/old.txt",
      }),
      expect.objectContaining({
        path: "diff:u:src/new.md",
        rel: "src/new.md",
        name: "new.md (working tree)",
        language: "markdown",
      }),
      expect.objectContaining({
        path: "diff:s:src/new.md",
        rel: "src/new.md",
        name: "new.md (staged)",
        language: "markdown",
      }),
    ]);
    expect(useStore.getState()).toMatchObject({
      activePath: "diff:s:src/new.md",
      selectedPath: "src/new.md",
      pendingTabClose: "src/new.md",
      pendingReveal: { path: "src/new.md", line: 4, column: 2 },
      allFiles: ["src/new.md"],
      cleanDump: { rel: "src/new.md" },
      dataTools: { rel: "src/new.md" },
    });
    expect(useStore.getState().searchResults?.matches[0].path).toBe("src/new.md");
    expect(useStore.getState().diagnostics?.items[0].path).toBe("src/new.md");
    expect(useStore.getState().gitStatus?.unstaged[0].path).toBe("src/new.md");
    expect(useStore.getState().toolResult).toMatchObject({ rel: "src/new.md", out: "src/old.sql" });
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["src/new.md"]));

    statusRefresh.resolve(gitStatus(["src/new.md"]));
    await vi.waitFor(() => expect(useStore.getState().gitBusy).toBe(false));
  });

  it("remaps an expanded directory, every descendant resource, and loaded tree key in one commit", async () => {
    const first = fileTab("docs/a.txt");
    const second = fileTab("docs/sub/b.txt", "draft", "base");
    const table = tableTab("docs/sub/data.csv");
    const diff = diffTab("docs/sub/b.txt", false);
    installWorkspace([first, second, table, diff]);
    const staleDescendantLoad = deferred<Entry[]>();
    mocks.listDir.mockImplementation((path: string) =>
      path === "docs/sub" ? staleDescendantLoad.promise : Promise.resolve([entry("manual", true)]),
    );
    useStore.setState({
      childrenByPath: {
        "": [entry("docs", true)],
        docs: [entry("docs/a.txt"), entry("docs/sub", true)],
        "docs/sub": [entry("docs/sub/b.txt"), entry("docs/sub/data.csv")],
      },
      expanded: { "": true, docs: true, "docs/sub": true },
      loadingPath: { docs: true, "docs/sub": true },
      selectedPath: "docs/sub",
      activePath: table.path,
      allFiles: ["docs/a.txt", "docs/sub/b.txt", "docs/sub/data.csv", "other.txt"],
      pendingFileOp: { type: "rename", targetRel: "docs", initial: "docs", title: "Rename" },
    });

    const loadingOldDescendant = useStore.getState().loadChildren("docs/sub");
    await vi.waitFor(() => expect(mocks.listDir).toHaveBeenCalledWith("docs/sub"));
    await useStore.getState().submitFileOp("manual");
    staleDescendantLoad.resolve([entry("docs/sub/stale.txt")]);
    await loadingOldDescendant;

    expect(useStore.getState().tabs.map((tab) => tab.path)).toEqual([
      "manual/a.txt",
      "manual/sub/b.txt",
      "manual/sub/data.csv",
      "diff:u:manual/sub/b.txt",
    ]);
    expect(useStore.getState().tabs[1]).toMatchObject({ content: "draft", savedContent: "base" });
    expect(useStore.getState().tabs[2]).toMatchObject({ rel: "manual/sub/data.csv", name: "data.csv" });
    expect(useStore.getState().activePath).toBe("manual/sub/data.csv");
    expect(useStore.getState().selectedPath).toBe("manual/sub");
    expect(Object.keys(useStore.getState().childrenByPath).sort()).toEqual(["", "manual", "manual/sub"]);
    expect(useStore.getState().childrenByPath["manual/sub"].map((item) => item.path)).toEqual([
      "manual/sub/b.txt",
      "manual/sub/data.csv",
    ]);
    expect(useStore.getState().expanded).toMatchObject({ manual: true, "manual/sub": true });
    expect(useStore.getState().loadingPath).toMatchObject({ manual: false, "manual/sub": false });
    expect(useStore.getState().allFiles).toEqual([
      "manual/a.txt",
      "manual/sub/b.txt",
      "manual/sub/data.csv",
      "other.txt",
    ]);
    await vi.waitFor(() =>
      expect(mocks.watch).toHaveBeenLastCalledWith(["manual/a.txt", "manual/sub/b.txt", "manual/sub/data.csv"]),
    );
  });
});

describe("resource-aware explorer delete reconciliation", () => {
  it("removes directory descendants and synthetic diff/table tabs, then drops deleted watcher paths", async () => {
    const keep = fileTab("keep.txt");
    const deletedFile = fileTab("gone/a.txt");
    const deletedTable = tableTab("gone/data.csv");
    const deletedDiff = diffTab("gone/a.txt", true);
    installWorkspace([keep, deletedFile, deletedTable, deletedDiff]);
    mocks.listDir.mockResolvedValue([entry("keep.txt")]);
    useStore.setState({
      activePath: deletedDiff.path,
      selectedPath: "gone/a.txt",
      childrenByPath: {
        "": [entry("gone", true), entry("keep.txt")],
        gone: [entry("gone/a.txt"), entry("gone/data.csv")],
      },
      expanded: { "": true, gone: true },
      loadingPath: { gone: true },
      allFiles: ["gone/a.txt", "gone/data.csv", "keep.txt"],
      searchResults: {
        matches: [
          { path: "gone/a.txt", line: 1, column: 1, text: "old" },
          { path: "keep.txt", line: 1, column: 1, text: "keep" },
        ],
        fileCount: 2,
        truncated: false,
      },
      diagnostics: {
        items: [
          { path: "gone/a.txt", line: 1, column: 1, severity: "warning", kind: "TODO", message: "old" },
          { path: "keep.txt", line: 1, column: 1, severity: "warning", kind: "TODO", message: "keep" },
        ],
        fileCount: 2,
        truncated: false,
      },
      cleanDump: { rel: "gone/a.txt" },
      dataTools: { rel: "gone/data.csv" },
      toolResult: { kind: "schema", title: "schema", rel: "gone/data.csv", schema: {} as never },
      pendingDelete: { rel: "gone", name: "gone" },
    });

    await useStore.getState().confirmDelete();

    expect(mocks.remove).toHaveBeenCalledWith("gone");
    expect(useStore.getState().tabs).toEqual([keep]);
    expect(useStore.getState().activePath).toBe("keep.txt");
    expect(useStore.getState().selectedPath).toBe("");
    expect(useStore.getState().childrenByPath).toEqual({ "": [entry("keep.txt")] });
    expect(useStore.getState().expanded).toEqual({ "": true });
    expect(useStore.getState().loadingPath).toEqual({ "": false });
    expect(useStore.getState().allFiles).toEqual(["keep.txt"]);
    expect(useStore.getState().searchResults?.matches.map((match) => match.path)).toEqual(["keep.txt"]);
    expect(useStore.getState().diagnostics?.items.map((item) => item.path)).toEqual(["keep.txt"]);
    expect(useStore.getState()).toMatchObject({ cleanDump: null, dataTools: null, toolResult: null });
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["keep.txt"]));
  });

  it("refuses to delete a directory containing an already-dirty editor", async () => {
    const dirty = fileTab("gone/sub/notes.txt", "unsaved", "saved");
    installWorkspace([dirty]);
    useStore.setState({ pendingDelete: { rel: "gone", name: "gone" } });

    await useStore.getState().confirmDelete();

    expect(mocks.remove).not.toHaveBeenCalled();
    expect(useStore.getState().tabs).toEqual([dirty]);
    expect(useStore.getState().status).toEqual({
      message: "Save or close affected unsaved editors before deleting this path.",
      kind: "error",
    });
  });

  it("keeps edits typed after confirmation as a recreatable unsaved draft", async () => {
    const original = fileTab("notes.txt", "saved", "saved");
    installWorkspace([original]);
    const deletion = deferred<void>();
    mocks.remove.mockReturnValue(deletion.promise);
    mocks.listDir.mockResolvedValue([]);
    useStore.setState({ pendingDelete: { rel: "notes.txt", name: "notes.txt" } });

    const deleting = useStore.getState().confirmDelete();
    await vi.waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("notes.txt"));
    useStore.getState().updateContent("notes.txt", "typed during delete");
    deletion.resolve();
    await deleting;

    expect(useStore.getState().tabs).toEqual([
      expect.objectContaining({
        path: "notes.txt",
        content: "typed during delete",
        savedContent: "saved",
        revision: "",
        staleOnDisk: true,
      }),
    ]);
    expect(useStore.getState().status?.message).toContain("kept as unsaved drafts");
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["notes.txt"]));
  });
});

describe("file-operation ordering and generation safety", () => {
  it("waits for an already-started save before dispatching rename", async () => {
    const dirty = fileTab("notes.txt", "draft", "saved");
    installWorkspace([dirty]);
    const write = deferred<{ path: string; revision: string }>();
    mocks.writeFile.mockReturnValue(write.promise);
    mocks.listDir.mockResolvedValue([entry("renamed.txt")]);

    const saving = useStore.getState().saveTab("notes.txt");
    await vi.waitFor(() => expect(mocks.writeFile).toHaveBeenCalled());
    useStore.setState({ pendingFileOp: { type: "rename", targetRel: "notes.txt", initial: "notes.txt", title: "Rename" } });
    const renaming = useStore.getState().submitFileOp("renamed.txt");
    expect(mocks.rename).not.toHaveBeenCalled();

    write.resolve({ path: "notes.txt", revision: "saved-draft" });
    await saving;
    await vi.waitFor(() => expect(mocks.rename).toHaveBeenCalledWith("notes.txt", "renamed.txt"));
    await renaming;

    expect(useStore.getState().tabs[0]).toMatchObject({
      path: "renamed.txt",
      content: "draft",
      savedContent: "draft",
      revision: "saved-draft",
    });
  });

  it("does not let a late rename response mutate a replacement workspace", async () => {
    const oldTab = fileTab("old.txt");
    installWorkspace([oldTab], 801);
    const rename = deferred<void>();
    mocks.rename.mockReturnValue(rename.promise);
    useStore.setState({ pendingFileOp: { type: "rename", targetRel: "old.txt", initial: "old.txt", title: "Rename" } });

    const renaming = useStore.getState().submitFileOp("new.txt");
    await vi.waitFor(() => expect(mocks.rename).toHaveBeenCalledWith("old.txt", "new.txt"));
    const newTab = fileTab("workspace-b.txt", "B", "B");
    installWorkspace([newTab], 802);
    rename.resolve();
    await renaming;

    expect(useStore.getState()).toMatchObject({ workspaceInstanceId: 802, root: "root-802", activePath: "workspace-b.txt" });
    expect(useStore.getState().tabs).toEqual([newTab]);
    expect(mocks.watch).not.toHaveBeenCalledWith(["new.txt"]);
  });

  it("does not let a late directory-delete response close tabs in a replacement workspace", async () => {
    installWorkspace([fileTab("old/a.txt")], 811);
    const deletion = deferred<void>();
    mocks.remove.mockReturnValue(deletion.promise);
    useStore.setState({ pendingDelete: { rel: "old", name: "old" } });

    const deleting = useStore.getState().confirmDelete();
    await vi.waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("old"));
    const newTab = fileTab("workspace-b.txt", "B", "B");
    installWorkspace([newTab], 812);
    deletion.resolve();
    await deleting;

    expect(useStore.getState()).toMatchObject({ workspaceInstanceId: 812, root: "root-812", activePath: "workspace-b.txt" });
    expect(useStore.getState().tabs).toEqual([newTab]);
    expect(mocks.watch).not.toHaveBeenCalledWith([]);
  });

  it("reconciles a rename whose bridge rejected after the filesystem commit", async () => {
    const original = fileTab("old.txt", "draft", "base");
    installWorkspace([original]);
    mocks.rename.mockRejectedValue(new Error("bridge response lost"));
    mocks.listDir.mockResolvedValue([entry("new.txt")]);
    useStore.setState({
      childrenByPath: { "": [entry("old.txt")] },
      pendingFileOp: { type: "rename", targetRel: "old.txt", initial: "old.txt", title: "Rename" },
    });

    await useStore.getState().submitFileOp("new.txt");

    expect(useStore.getState().tabs[0]).toMatchObject({ path: "new.txt", content: "draft", savedContent: "base" });
    expect(useStore.getState().status?.message).toContain("Rename completed, but confirmation failed");
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["new.txt"]));
  });

  it("does not let a pre-rename search response restore an obsolete path", async () => {
    const original = fileTab("old.txt");
    installWorkspace([original]);
    const search = deferred<{ matches: { path: string; line: number; column: number; text: string }[]; fileCount: number; truncated: boolean }>();
    mocks.search.mockReturnValue(search.promise);
    mocks.listDir.mockResolvedValue([entry("new.txt")]);
    useStore.setState({
      searchResults: {
        matches: [{ path: "old.txt", line: 1, column: 1, text: "existing" }],
        fileCount: 1,
        truncated: false,
      },
    });

    const searching = useStore.getState().runSearch("needle");
    useStore.setState({ pendingFileOp: { type: "rename", targetRel: "old.txt", initial: "old.txt", title: "Rename" } });
    await useStore.getState().submitFileOp("new.txt");
    search.resolve({
      matches: [{ path: "old.txt", line: 2, column: 1, text: "late" }],
      fileCount: 1,
      truncated: false,
    });
    await searching;

    expect(useStore.getState().searchResults?.matches).toEqual([
      expect.objectContaining({ path: "new.txt", text: "existing" }),
    ]);
    expect(useStore.getState().searching).toBe(false);
  });

  it("keeps only the newest workspace search across an A to B to A query cycle", async () => {
    installWorkspace([fileTab("notes.txt")]);
    type Result = {
      matches: { path: string; line: number; column: number; text: string }[];
      fileCount: number;
      truncated: boolean;
    };
    const firstA = deferred<Result>();
    const middleB = deferred<Result>();
    const secondA = deferred<Result>();
    mocks.search
      .mockReturnValueOnce(firstA.promise)
      .mockReturnValueOnce(middleB.promise)
      .mockReturnValueOnce(secondA.promise);

    const firstPending = useStore.getState().runSearch("alpha");
    useStore.getState().setSearchQuery("beta");
    const middlePending = useStore.getState().runSearch("beta");
    useStore.getState().setSearchQuery("alpha");
    const latestPending = useStore.getState().runSearch("alpha");

    secondA.resolve({
      matches: [{ path: "notes.txt", line: 3, column: 1, text: "latest alpha" }],
      fileCount: 1,
      truncated: false,
    });
    await latestPending;
    firstA.resolve({
      matches: [{ path: "notes.txt", line: 1, column: 1, text: "stale alpha" }],
      fileCount: 1,
      truncated: false,
    });
    middleB.resolve({
      matches: [{ path: "notes.txt", line: 2, column: 1, text: "stale beta" }],
      fileCount: 1,
      truncated: false,
    });
    await Promise.all([firstPending, middlePending]);

    expect(useStore.getState().searchResults?.matches).toEqual([
      expect.objectContaining({ line: 3, text: "latest alpha" }),
    ]);
    expect(useStore.getState().searching).toBe(false);
  });

  it("watches table resources and invalidates their window and derived indexes on change", async () => {
    const table = tableTab("data/items.csv");
    installWorkspace([table]);
    useStore.setState({
      allFiles: ["data/items.csv"],
      searchResults: {
        matches: [{ path: "data/items.csv", line: 2, column: 1, text: "old" }],
        fileCount: 1,
        truncated: false,
      },
      diagnostics: {
        items: [{ path: "data/items.csv", line: 2, column: 1, severity: "warning", kind: "TODO", message: "old" }],
        fileCount: 1,
        truncated: false,
      },
    });

    useStore.getState().syncWatches();
    await vi.waitFor(() => expect(mocks.watch).toHaveBeenLastCalledWith(["data/items.csv"]));
    await useStore.getState().reloadIfChanged("data/items.csv");

    expect(useStore.getState().tabs[0].sourceVersion).toBe(1);
    expect(useStore.getState()).toMatchObject({ allFiles: [], searchResults: null, diagnostics: null });
    expect(mocks.readFile).not.toHaveBeenCalled();
  });
});
