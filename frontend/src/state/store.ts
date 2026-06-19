import { create } from "zustand";
import { Workspace, Settings, Git, LLM, Agent, Db, Watcher, Jobs, Artifacts, SecretService, Shell, errMessage, STALE_MARKER } from "../lib/services";
import type {
  Entry,
  SettingsModel,
  EditorSettings,
  GitStatus,
  DbProfile,
  DbTable,
  SearchResult,
  DiagnosticsResult,
  SchemaResult,
  DumpSummary,
  DumpTransform,
  AuditEntry,
  Job,
  Artifact,
} from "../lib/services";
import { languageForPath, isTabular } from "../lib/lang";
import { validateFileName } from "../lib/validate";

// Result of a Tools-menu utility, shown in the ToolsModal.
export type ToolResult =
  | { kind: "schema"; title: string; rel: string; schema: SchemaResult }
  | { kind: "dump"; title: string; rel: string; dump: DumpSummary }
  | { kind: "sql"; title: string; rel: string; table: string; out: string; sql: string }
  | { kind: "error"; title: string; message: string };

export type ViewId = "explorer" | "search" | "git" | "db" | "artifacts" | "settings";
export type StatusKind = "info" | "error" | "success";
export type TabKind = "file" | "diff" | "db" | "table";

export interface ChatMsg {
  id: string;
  role: "user" | "assistant" | "tool";
  content: string;
  streaming?: boolean;
  error?: boolean;
  // tool messages (agent mode)
  tool?: string;
  args?: string;
  result?: string;
  callId?: string;
  runId?: string;
  approval?: "pending" | "approved" | "denied";
  // continuePrompt messages are the "keep going at a step checkpoint?" cards
  // (content holds the step count); they reuse the approval flow.
  continuePrompt?: boolean;
}

export type PlanStatus = "todo" | "in_progress" | "done";
export interface PlanStep {
  title: string;
  status: PlanStatus;
}

export interface AgentEvent {
  runId: string;
  type: string;
  text?: string;
  callId?: string;
  tool?: string;
  args?: string;
  result?: string;
  plan?: PlanStep[];
}

export interface LLMConfig {
  provider: string;
  baseURL: string;
  model: string;
  apiKeyRef: string;
  requestTimeoutSec: number;
}

let uidCounter = 0;
const uid = () => `m${++uidCounter}`;
const API_KEY_REF = "llm.apikey";
// Ask-mode chat: cap how many recent user/assistant turns are re-sent each
// message so a long conversation can't blow past the model's context window.
const MAX_CHAT_HISTORY = 20;

function applyUIFont(px: number) {
  document.documentElement.style.setProperty("--ui-base", `${px || 13}px`);
}

// Strip any embedded "user:pass@" credentials from a provider base URL so they
// are never persisted in cleartext settings — the API key belongs in the secret
// store, not the URL.
function stripUrlCreds(raw: string): string {
  try {
    const u = new URL(raw);
    if (u.username || u.password) {
      u.username = "";
      u.password = "";
      return u.toString();
    }
  } catch {
    /* not a full URL yet (still being typed) — leave as-is */
  }
  return raw;
}

const dirOf = (rel: string): string => {
  const i = rel.lastIndexOf("/");
  return i >= 0 ? rel.slice(0, i) : "";
};
const remapPath = (path: string, oldRel: string, newRel: string): string => {
  if (path === oldRel) return newRel;
  if (path.startsWith(oldRel + "/")) return newRel + path.slice(oldRel.length);
  return path;
};

// Remap any tab kind (file/table/diff) when a file or its parent folder is renamed.
function remapTab(t: Tab, oldRel: string, newRel: string): Tab {
  if (t.kind === "file") return { ...t, path: remapPath(t.path, oldRel, newRel) };
  if (t.kind === "table" && t.rel) {
    const rel = remapPath(t.rel, oldRel, newRel);
    return { ...t, rel, path: rel };
  }
  if (t.kind === "diff" && t.rel) {
    const rel = remapPath(t.rel, oldRel, newRel);
    // Preserve the staged/unstaged discriminator embedded in the tab key.
    const prefix = t.path.startsWith("diff:s:") ? "diff:s:" : "diff:u:";
    return { ...t, rel, path: `${prefix}${rel}` };
  }
  return t; // db tabs aren't affected by file renames
}

export interface FileMenu {
  x: number;
  y: number;
  rel: string;
  isDir: boolean;
  name: string;
}
export interface PendingFileOp {
  type: "newFile" | "newFolder" | "rename";
  targetRel: string; // parent dir for new ops; the entry itself for rename
  initial: string;
  title: string;
}
export interface PendingDelete {
  rel: string;
  name: string;
}

export interface Tab {
  path: string; // unique key — files use rel path; diffs use "diff:<rel>"
  name: string;
  kind: TabKind;
  language: string;
  // file tabs
  content: string;
  savedContent: string;
  revision: string;
  binary: boolean;
  tooLarge: boolean;
  encoding?: string; // detected on-disk encoding (utf-8, utf-16le, latin-1, …); save round-trips it
  staleOnDisk?: boolean; // changed on disk by another program
  // diff tabs
  diffOld?: string;
  diffNew?: string;
  rel?: string;
  // db tabs
  connId?: string;
}

export interface Status {
  message: string;
  kind: StatusKind;
}

function baseName(p: string): string {
  const parts = p.replace(/\\/g, "/").split("/").filter(Boolean);
  return parts[parts.length - 1] || p;
}

interface State {
  // workspace
  root: string;
  wsName: string;
  isOpen: boolean;
  recents: string[];

  // file tree (lazy): "" key is the root
  childrenByPath: Record<string, Entry[]>;
  expanded: Record<string, boolean>;
  loadingPath: Record<string, boolean>;
  selectedPath: string | null;

  // editor
  tabs: Tab[];
  activePath: string | null;

  // git
  gitStatus: GitStatus | null;
  gitBusy: boolean;

  // assistant (LLM chat)
  assistantVisible: boolean;
  agentMode: boolean;
  chat: ChatMsg[];
  chatStreaming: boolean;
  models: string[];
  streamReqId: string | null;
  streamMsgId: string | null;
  agentRunId: string | null;
  agentStarting: boolean; // true between Agent.Start dispatch and the run's first event
  agentPlan: PlanStep[];

  // database
  dbProfiles: DbProfile[];
  activeDbId: string | null;
  dbTables: DbTable[];
  dbTablesLoading: boolean; // tables for the active connection are being fetched
  dbSql: Record<string, string>;
  dbHistory: Record<string, string[]>;

  // explorer file operations
  fileMenu: FileMenu | null;
  pendingFileOp: PendingFileOp | null;
  pendingDelete: PendingDelete | null;

  // search
  searchQuery: string;
  searchResults: SearchResult | null;
  searching: boolean;
  pendingReveal: { path: string; line: number; column: number } | null;

  // tab close confirmation (set when closing a tab with unsaved edits)
  pendingTabClose: string | null;

  // Tools menu (CSV/dump utilities) result shown in a modal
  toolResult: ToolResult | null;
  toolBusy: boolean;
  cleanDump: { rel: string } | null; // active "Clean SQL dump" form target

  // ui
  view: ViewId;
  sidebarVisible: boolean;
  panelVisible: boolean;
  panelMounted: boolean; // stays true after first open so the terminal PTY survives toggles
  panelTab: "terminal" | "problems" | "jobs";
  jobs: Job[];
  artifacts: Artifact[];
  sidebarWidth: number;
  assistantWidth: number;
  panelHeight: number;
  paletteOpen: boolean;
  paletteMode: "files" | "commands";
  allFiles: string[];
  loadingAllFiles: boolean; // quick-open index is being (re)built
  auditOpen: boolean;
  auditEntries: AuditEntry[];
  auditLoading: boolean;
  allFilesError: string | null; // last ListAllFiles failure, for an indexing-error UI
  diagnostics: DiagnosticsResult | null;
  loadingDiagnostics: boolean;
  status: Status | null;
  settings: SettingsModel | null;

  // actions — workspace/tree
  init: () => Promise<void>;
  pickAndOpen: () => Promise<void>;
  openWorkspace: (path: string) => Promise<void>;
  closeWorkspace: () => void;
  loadChildren: (path: string) => Promise<void>;
  toggleDir: (path: string) => Promise<void>;
  refreshTree: () => Promise<void>;

  // actions — editor
  openFile: (path: string, name: string) => Promise<void>;
  setActive: (path: string) => void;
  closeTab: (path: string) => void;
  requestCloseTab: (path: string) => void;
  confirmCloseTab: (save: boolean) => Promise<void>;
  cancelCloseTab: () => void;
  runCsvSchema: () => Promise<void>;
  runDumpAnalyze: () => Promise<void>;
  runCsvToSql: () => Promise<void>;
  saveCsvToSql: (rel: string, outRel: string, table: string) => Promise<void>;
  extractDumpTable: (rel: string, table: string) => Promise<void>;
  dumpTableToCsv: (rel: string, table: string) => Promise<void>;
  splitDump: (rel: string) => Promise<void>;
  exportCsvColumns: (rel: string, columns: string[]) => Promise<void>;
  addCsvColumn: (rel: string, name: string, value: string) => Promise<void>;
  openCleanDump: () => void;
  applyCleanDump: (rel: string, outRel: string, t: DumpTransform) => Promise<void>;
  cancelCleanDump: () => void;
  closeToolResult: () => void;
  updateContent: (path: string, content: string) => void;
  saveActive: () => Promise<void>;
  saveTab: (path: string) => Promise<void>;
  convertEncoding: (path: string, encoding: string) => Promise<void>;
  reloadIfChanged: (path: string) => Promise<void>;
  reloadTab: (path: string) => Promise<void>;
  syncWatches: () => void;

  // actions — git
  loadGitStatus: () => Promise<void>;
  stageFile: (rel: string) => Promise<void>;
  unstageFile: (rel: string) => Promise<void>;
  stageAll: () => Promise<void>;
  commit: (subject: string, body: string) => Promise<boolean>;
  openDiff: (rel: string, oldRel?: string, staged?: boolean) => Promise<void>;

  // actions — assistant
  toggleAssistant: () => void;
  toggleAgentMode: () => void;
  sendChat: (text: string) => Promise<void>;
  sendAgent: (text: string) => Promise<void>;
  approveAgent: (callId: string, approved: boolean) => Promise<void>;
  handleAgentEvent: (ev: AgentEvent) => void;
  cancelChat: () => void;
  clearChat: () => void;
  appendDelta: (reqId: string, delta: string) => void;
  finishStream: (reqId: string) => void;
  failStream: (reqId: string, message: string) => void;
  loadModels: (silent?: boolean) => Promise<void>;
  saveLLMConfig: (patch: Partial<LLMConfig>) => Promise<void>;
  saveEditorConfig: (patch: Partial<EditorSettings>) => Promise<void>;
  setUIFontSize: (n: number) => Promise<void>;
  setApiKey: (value: string) => Promise<void>;

  // actions — database
  loadDbProfiles: () => Promise<void>;
  saveDbProfile: (p: DbProfile) => Promise<DbProfile | null>;
  deleteDbProfile: (id: string) => Promise<void>;
  testDb: (id: string) => Promise<void>;
  selectDb: (id: string) => Promise<void>;
  openDbQuery: (id: string) => void;
  setDbSql: (id: string, sql: string) => void;
  pushDbHistory: (id: string, sql: string) => void;

  // actions — explorer file operations
  openFileMenu: (m: FileMenu) => void;
  closeFileMenu: () => void;
  startNewFile: (parentRel: string) => void;
  startNewFolder: (parentRel: string) => void;
  startRename: (rel: string, name: string) => void;
  submitFileOp: (value: string) => Promise<void>;
  cancelFileOp: () => void;
  requestDelete: (rel: string, name: string) => void;
  confirmDelete: () => Promise<void>;
  cancelDelete: () => void;

  // actions — search
  setSearchQuery: (query: string) => void;
  runSearch: (query: string) => Promise<void>;
  openFileAt: (path: string, line: number, column: number) => Promise<void>;
  clearReveal: () => void;

  // actions — ui
  setView: (v: ViewId) => void;
  toggleSidebar: () => void;
  togglePanel: () => void;
  openPalette: (mode: "files" | "commands") => void;
  closePalette: () => void;
  openAuditLog: () => Promise<void>;
  closeAuditLog: () => void;
  loadAllFiles: () => Promise<void>;
  setPanelTab: (tab: "terminal" | "problems" | "jobs") => void;
  loadJobs: () => Promise<void>;
  cancelJob: (id: string) => Promise<void>;
  clearFinishedJobs: () => Promise<void>;
  loadArtifacts: () => Promise<void>;
  setArtifactArchived: (id: string, archived: boolean) => Promise<void>;
  deleteArtifact: (id: string) => Promise<void>;
  saveActiveAsArtifact: (kind: string) => Promise<void>;
  runDiagnostics: () => Promise<void>;
  showProblems: () => void;
  resizeSidebar: (delta: number) => void;
  resizeAssistant: (delta: number) => void;
  resizePanel: (delta: number) => void;
  setStatus: (message: string, kind?: StatusKind) => void;
  dismissStatus: () => void;
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

let statusTimer: ReturnType<typeof setTimeout> | undefined;

export const useStore = create<State>()((set, get) => ({
  root: "",
  wsName: "",
  isOpen: false,
  recents: [],
  childrenByPath: {},
  expanded: {},
  loadingPath: {},
  selectedPath: null,
  tabs: [],
  activePath: null,
  gitStatus: null,
  gitBusy: false,
  assistantVisible: true,
  agentMode: false,
  chat: [],
  chatStreaming: false,
  models: [],
  streamReqId: null,
  streamMsgId: null,
  agentRunId: null,
  agentStarting: false,
  agentPlan: [],
  dbProfiles: [],
  activeDbId: null,
  dbTables: [],
  dbTablesLoading: false,
  dbSql: {},
  dbHistory: {},
  fileMenu: null,
  pendingFileOp: null,
  pendingDelete: null,
  searchQuery: "",
  searchResults: null,
  searching: false,
  pendingReveal: null,
  pendingTabClose: null,
  toolResult: null,
  toolBusy: false,
  cleanDump: null,
  view: "explorer",
  sidebarVisible: true,
  panelVisible: false,
  panelMounted: false,
  panelTab: "terminal",
  jobs: [],
  artifacts: [],
  sidebarWidth: 280,
  assistantWidth: 360,
  panelHeight: 240,
  paletteOpen: false,
  auditOpen: false,
  auditEntries: [],
  auditLoading: false,
  paletteMode: "files",
  allFiles: [],
  loadingAllFiles: false,
  allFilesError: null,
  diagnostics: null,
  loadingDiagnostics: false,
  status: null,
  settings: null,

  init: async () => {
    try {
      const s = await Settings.Load();
      set({ settings: s, recents: s.recentWorkspaces ?? [] });
      applyUIFont(s.uiFontSize);
      void get().loadModels(true); // populate the model list on startup (silent if provider is down)
      if (s.lastWorkspace) {
        try {
          await get().openWorkspace(s.lastWorkspace);
        } catch {
          /* last workspace gone — show welcome */
        }
      }
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  pickAndOpen: async () => {
    try {
      const dir = await Shell.SelectFolder();
      if (dir) await get().openWorkspace(dir);
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  openWorkspace: async (path: string) => {
    const info = await Workspace.Open(path);
    set({
      root: info.root,
      wsName: info.name,
      isOpen: true,
      childrenByPath: {},
      expanded: { "": true },
      loadingPath: {},
      selectedPath: null,
      tabs: [],
      activePath: null,
      gitStatus: null,
      allFiles: [],
    });
    try {
      // Refresh the FULL settings snapshot so later whole-object saves
      // (saveLLMConfig/setApiKey) don't clobber lastWorkspace/recents on disk.
      const s = await Settings.RememberWorkspace(path);
      set({ settings: s, recents: s.recentWorkspaces ?? [] });
    } catch {
      /* non-fatal */
    }
    get().syncWatches();
    await get().loadChildren("");
    void get().loadGitStatus();
    void get().runDiagnostics();
  },

  closeWorkspace: () => {
    // Cancel any in-flight LLM/agent run and resolve pending approvals.
    get().cancelChat();
    void Workspace.Close();
    void Watcher.Watch([]); // stop the backend watcher for the closing workspace
    set({
      root: "",
      wsName: "",
      isOpen: false,
      childrenByPath: {},
      expanded: {},
      tabs: [],
      activePath: null,
      selectedPath: null,
      gitStatus: null,
      // Clear per-workspace assistant / db-session / search / diagnostics / index
      // state so a newly opened workspace can't inherit the previous one's data.
      chat: [],
      agentPlan: [],
      activeDbId: null,
      dbTables: [],
      dbTablesLoading: false,
      dbSql: {},
      searchQuery: "",
      searchResults: null,
      diagnostics: null,
      allFiles: [],
      allFilesError: null,
    });
  },

  loadChildren: async (path: string) => {
    set((st) => ({ loadingPath: { ...st.loadingPath, [path]: true } }));
    try {
      const entries = await Workspace.ListDir(path);
      set((st) => ({ childrenByPath: { ...st.childrenByPath, [path]: entries } }));
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set((st) => ({ loadingPath: { ...st.loadingPath, [path]: false } }));
    }
  },

  toggleDir: async (path: string) => {
    const open = !get().expanded[path];
    set((st) => ({ expanded: { ...st.expanded, [path]: open }, selectedPath: path }));
    if (open && !get().childrenByPath[path]) {
      await get().loadChildren(path);
    }
  },

  refreshTree: async () => {
    const loaded = Object.keys(get().childrenByPath);
    // Reload the expanded directories concurrently rather than serially.
    await Promise.all(loaded.map((p) => get().loadChildren(p)));
    void get().loadGitStatus();
  },

  openFile: async (path: string, name: string) => {
    set({ selectedPath: path });
    const existing = get().tabs.find((t) => t.path === path);
    if (existing) {
      set({ activePath: path });
      return;
    }
    // appendTab adds the tab only if one for this path doesn't already exist.
    // It's the commit-time dedup guard: two rapid opens of the same file can
    // both pass the check above before either's set() runs (the ReadFile await
    // below widens that window for big files), which previously created two tabs
    // with the same path — duplicate React keys that render "doubled" and resist
    // closing.
    const appendTab = (tab: Tab) =>
      set((st) => (st.tabs.some((t) => t.path === path) ? { activePath: path } : { tabs: [...st.tabs, tab], activePath: path }));

    // Tabular files open in the analytics grid (TableView fetches by rel).
    if (isTabular(path)) {
      appendTab({
        path,
        name,
        kind: "table",
        language: "",
        content: "",
        savedContent: "",
        revision: "",
        binary: false,
        tooLarge: false,
        rel: path,
      });
      return;
    }
    try {
      const fc = await Workspace.ReadFile(path);
      appendTab({
        path,
        name,
        kind: "file",
        language: languageForPath(path),
        content: fc.content,
        savedContent: fc.content,
        revision: fc.revision,
        binary: fc.binary,
        tooLarge: fc.tooLarge,
        encoding: fc.encoding,
      });
      get().syncWatches();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  syncWatches: () => {
    const files = get().tabs.filter((t) => t.kind === "file").map((t) => t.path);
    void Watcher.Watch(files);
  },

  reloadIfChanged: async (path: string) => {
    if (!get().tabs.some((t) => t.path === path && t.kind === "file")) return;
    try {
      const fc = await Workspace.ReadFile(path);
      // Re-read the tab from CURRENT state inside the updater: a save that landed
      // during the await above must win, otherwise it could be resurrected as
      // staleOnDisk against a pre-await snapshot.
      set((st) => {
        const tab = st.tabs.find((t) => t.path === path && t.kind === "file");
        if (!tab) return {}; // tab closed during the await
        if (fc.revision === tab.revision) {
          if (!tab.staleOnDisk) return {};
          return { tabs: st.tabs.map((t) => (t.path === path ? { ...t, staleOnDisk: false } : t)) };
        }
        if (tab.content !== tab.savedContent) {
          // Don't clobber unsaved edits — flag a conflict the user resolves.
          return { tabs: st.tabs.map((t) => (t.path === path ? { ...t, staleOnDisk: true } : t)) };
        }
        return {
          tabs: st.tabs.map((t) =>
            t.path === path
              ? { ...t, content: fc.content, savedContent: fc.content, revision: fc.revision, binary: fc.binary, tooLarge: fc.tooLarge, encoding: fc.encoding, staleOnDisk: false }
              : t,
          ),
        };
      });
    } catch {
      /* file removed or unreadable — leave the tab as-is */
    }
  },

  reloadTab: async (path: string) => {
    try {
      const fc = await Workspace.ReadFile(path);
      set((st) => ({
        tabs: st.tabs.map((t) =>
          t.path === path
            ? { ...t, content: fc.content, savedContent: fc.content, revision: fc.revision, binary: fc.binary, tooLarge: fc.tooLarge, encoding: fc.encoding, staleOnDisk: false }
            : t,
        ),
      }));
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  setActive: (path: string) => set({ activePath: path, selectedPath: path }),

  closeTab: (path: string) => {
    const { tabs, activePath } = get();
    const idx = tabs.findIndex((t) => t.path === path);
    if (idx < 0) return;
    const next = tabs.filter((t) => t.path !== path);
    let newActive = activePath;
    if (activePath === path) {
      newActive = next.length ? next[Math.min(idx, next.length - 1)].path : null;
    }
    set({ tabs: next, activePath: newActive });
    get().syncWatches();
  },

  requestCloseTab: (path: string) => {
    const tab = get().tabs.find((t) => t.path === path);
    const dirty = !!tab && tab.kind === "file" && !tab.binary && !tab.tooLarge && tab.content !== tab.savedContent;
    if (dirty) {
      set({ pendingTabClose: path });
    } else {
      get().closeTab(path);
    }
  },

  confirmCloseTab: async (save: boolean) => {
    const path = get().pendingTabClose;
    set({ pendingTabClose: null });
    if (!path) return;
    if (save) await get().saveTab(path);
    get().closeTab(path);
  },

  cancelCloseTab: () => set({ pendingTabClose: null }),

  runCsvSchema: async () => {
    const path = get().activePath;
    if (!path || !/\.(csv|tsv)$/i.test(path)) {
      get().setStatus("Open a CSV or TSV file first to infer its schema.", "error");
      return;
    }
    const name = path.split("/").pop() ?? path;
    set({ toolBusy: true, toolResult: null });
    try {
      const schema = await Workspace.InferTableSchema(path, "");
      set({ toolResult: { kind: "schema", title: `Schema · ${name}`, rel: path, schema } });
    } catch (e) {
      set({ toolResult: { kind: "error", title: `Schema · ${name}`, message: errMessage(e) } });
    } finally {
      set({ toolBusy: false });
    }
  },

  runDumpAnalyze: async () => {
    const path = get().activePath;
    if (!path || !/\.(sql|dump)$/i.test(path)) {
      get().setStatus("Open a .sql or .dump file first to analyze it.", "error");
      return;
    }
    const name = path.split("/").pop() ?? path;
    set({ toolBusy: true, toolResult: null });
    try {
      const dump = await Workspace.AnalyzeSQLDump(path);
      set({ toolResult: { kind: "dump", title: `SQL dump · ${name}`, rel: path, dump } });
    } catch (e) {
      set({ toolResult: { kind: "error", title: `SQL dump · ${name}`, message: errMessage(e) } });
    } finally {
      set({ toolBusy: false });
    }
  },

  runCsvToSql: async () => {
    const path = get().activePath;
    if (!path || !/\.(csv|tsv)$/i.test(path)) {
      get().setStatus("Open a CSV or TSV file first to convert it to SQL.", "error");
      return;
    }
    const name = path.split("/").pop() ?? path;
    const table = name.replace(/\.[^.]+$/, "");
    const dir = dirOf(path);
    const out = dir ? `${dir}/${table}.sql` : `${table}.sql`;
    set({ toolBusy: true, toolResult: null });
    try {
      const sql = await Workspace.PreviewCsvToSql(path, table, true);
      set({ toolResult: { kind: "sql", title: `CSV → SQL · ${name}`, rel: path, table, out, sql } });
    } catch (e) {
      set({ toolResult: { kind: "error", title: `CSV → SQL · ${name}`, message: errMessage(e) } });
    } finally {
      set({ toolBusy: false });
    }
  },

  saveCsvToSql: async (rel, outRel, table) => {
    set({ toolBusy: true });
    try {
      const sum = await Workspace.ConvertCsvToSql(rel, outRel, table || "data", true);
      set({ toolResult: null });
      get().setStatus(`Wrote ${outRel} (${sum.rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  extractDumpTable: async (rel, table) => {
    const dir = dirOf(rel);
    const out = dir ? `${dir}/${table}.sql` : `${table}.sql`;
    set({ toolBusy: true });
    try {
      const res = await Workspace.ExtractDumpTable(rel, table, out);
      set({ toolResult: null });
      get().setStatus(`Extracted ${table} → ${out} (${res.bytes.toLocaleString()} bytes)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  splitDump: async (rel) => {
    const name = (rel.split("/").pop() ?? rel).replace(/\.[^.]+$/, "");
    const dir = dirOf(rel);
    const outDir = dir ? `${dir}/${name}-tables` : `${name}-tables`;
    set({ toolBusy: true });
    try {
      const res = await Workspace.SplitDump(rel, outDir);
      set({ toolResult: null });
      get().setStatus(`Split into ${res.tables} files under ${outDir}`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  dumpTableToCsv: async (rel, table) => {
    const dir = dirOf(rel);
    const out = dir ? `${dir}/${table}.csv` : `${table}.csv`;
    set({ toolBusy: true });
    try {
      const rows = await Workspace.DumpTableToCsv(rel, table, out);
      set({ toolResult: null });
      get().setStatus(`Extracted ${table} → ${out} (${rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  addCsvColumn: async (rel, name, value) => {
    if (!name.trim()) {
      get().setStatus("Enter a column name.", "error");
      return;
    }
    const base = (rel.split("/").pop() ?? rel).replace(/\.[^.]+$/, "");
    const dir = dirOf(rel);
    const out = dir ? `${dir}/${base}-plus.csv` : `${base}-plus.csv`;
    set({ toolBusy: true });
    try {
      const rows = await Workspace.AddCsvColumn(rel, out, name.trim(), value);
      set({ toolResult: null });
      get().setStatus(`Wrote ${out} (added "${name.trim()}", ${rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  exportCsvColumns: async (rel, columns) => {
    if (columns.length === 0) {
      get().setStatus("Select at least one column to export.", "error");
      return;
    }
    const name = (rel.split("/").pop() ?? rel).replace(/\.[^.]+$/, "");
    const dir = dirOf(rel);
    const out = dir ? `${dir}/${name}-cols.csv` : `${name}-cols.csv`;
    set({ toolBusy: true });
    try {
      const rows = await Workspace.ProjectCsv(rel, out, columns);
      set({ toolResult: null });
      get().setStatus(`Wrote ${out} (${columns.length} cols, ${rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  openCleanDump: () => {
    const path = get().activePath;
    if (!path || !/\.(sql|dump)$/i.test(path)) {
      get().setStatus("Open a .sql or .dump file first to clean it.", "error");
      return;
    }
    set({ cleanDump: { rel: path } });
  },

  cancelCleanDump: () => set({ cleanDump: null }),

  applyCleanDump: async (rel, outRel, t) => {
    set({ toolBusy: true });
    try {
      const sum = await Workspace.TransformDump(rel, outRel, t);
      set({ cleanDump: null });
      get().setStatus(`Wrote ${outRel} (${sum.replacements.toLocaleString()} replacements)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ toolBusy: false });
    }
  },

  closeToolResult: () => set({ toolResult: null }),

  updateContent: (path: string, content: string) =>
    set((st) => ({ tabs: st.tabs.map((t) => (t.path === path ? { ...t, content } : t)) })),

  saveActive: async () => {
    const { activePath } = get();
    if (activePath) await get().saveTab(activePath);
  },

  // Rewrite the file in a different on-disk encoding (e.g. UTF-16 -> UTF-8). The
  // text is unchanged; this is an explicit, immediate re-encode of the bytes.
  convertEncoding: async (path: string, encoding: string) => {
    const tab = get().tabs.find((t) => t.path === path);
    if (!tab || tab.kind !== "file" || tab.binary || tab.tooLarge) return;
    if ((tab.encoding ?? "utf-8") === encoding) return;
    try {
      const res = await Workspace.WriteFile(path, tab.content, tab.revision, encoding);
      set((st) => ({
        tabs: st.tabs.map((t) =>
          t.path === path ? { ...t, encoding, savedContent: t.content, revision: res.revision } : t,
        ),
      }));
      get().setStatus(`Saved ${tab.name} as ${encoding}`, "success");
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  saveTab: async (path: string) => {
    const tab = get().tabs.find((t) => t.path === path);
    if (!tab || tab.kind !== "file" || tab.binary || tab.tooLarge) return;
    if (tab.content === tab.savedContent) return;
    try {
      // Round-trip the file's detected encoding so saving a UTF-16/Latin-1 file
      // doesn't silently rewrite it as UTF-8 (empty = UTF-8 for new files).
      const res = await Workspace.WriteFile(path, tab.content, tab.revision, tab.encoding ?? "");
      set((st) => ({
        tabs: st.tabs.map((t) =>
          t.path === path ? { ...t, savedContent: t.content, revision: res.revision } : t,
        ),
      }));
      get().setStatus(`Saved ${tab.name}`, "success");
      void get().loadGitStatus();
    } catch (e) {
      const msg = errMessage(e);
      if (msg.toLowerCase().includes(STALE_MARKER)) {
        get().setStatus(`${tab.name} changed on disk — save refused. Reopen to reload.`, "error");
      } else {
        get().setStatus(msg, "error");
      }
    }
  },

  loadGitStatus: async () => {
    if (!get().isOpen) {
      set({ gitStatus: null });
      return;
    }
    set({ gitBusy: true });
    try {
      const st = await Git.Status();
      set({ gitStatus: st });
    } catch (e) {
      const message = errMessage(e);
      // Surface the failure in the panel instead of leaving it stuck on
      // "Loading…" forever when the very first status call throws.
      set({
        gitStatus: { available: false, branch: "", head: "", aheadBehind: "", staged: [], unstaged: [], message },
      });
      get().setStatus(message, "error");
    } finally {
      set({ gitBusy: false });
    }
  },

  stageFile: async (rel: string) => {
    if (get().gitBusy) return;
    set({ gitBusy: true });
    try {
      set({ gitStatus: await Git.Stage(rel) });
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ gitBusy: false });
    }
  },

  unstageFile: async (rel: string) => {
    if (get().gitBusy) return;
    set({ gitBusy: true });
    try {
      set({ gitStatus: await Git.Unstage(rel) });
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ gitBusy: false });
    }
  },

  stageAll: async () => {
    if (get().gitBusy) return;
    set({ gitBusy: true });
    try {
      set({ gitStatus: await Git.StageAll() });
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ gitBusy: false });
    }
  },

  commit: async (subject: string, body: string) => {
    try {
      const res = await Git.Commit(subject, body);
      if (res.status && res.status.available) set({ gitStatus: res.status });
      if (res.hash) {
        get().setStatus(`Committed ${res.shortHash}`, "success");
        return true;
      }
      get().setStatus(res.message, "info");
      return false;
    } catch (e) {
      get().setStatus(errMessage(e), "error");
      return false;
    }
  },

  openDiff: async (rel: string, oldRel = "", staged = false) => {
    // Key by staged state so the staged and unstaged diffs of one file open as
    // two distinct tabs instead of overwriting each other.
    const key = `diff:${staged ? "s" : "u"}:${rel}`;
    const label = `${baseName(rel)} ${staged ? "(staged)" : "(working tree)"}`;
    try {
      const d = await Git.Diff(rel, oldRel, staged);
      const existing = get().tabs.find((t) => t.path === key);
      if (existing) {
        set((st) => ({
          tabs: st.tabs.map((t) =>
            t.path === key ? { ...t, name: label, binary: d.binary, diffOld: d.oldText, diffNew: d.newText } : t,
          ),
          activePath: key,
        }));
        return;
      }
      const tab: Tab = {
        path: key,
        name: label,
        kind: "diff",
        language: languageForPath(rel),
        content: "",
        savedContent: "",
        revision: "",
        binary: d.binary,
        tooLarge: false,
        diffOld: d.oldText,
        diffNew: d.newText,
        rel,
      };
      set((st) => ({ tabs: [...st.tabs, tab], activePath: key }));
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  toggleAssistant: () => set((st) => ({ assistantVisible: !st.assistantVisible })),
  toggleAgentMode: () => set((st) => ({ agentMode: !st.agentMode })),

  sendAgent: async (text: string) => {
    const content = text.trim();
    if (!content || get().chatStreaming) return;
    const userMsg: ChatMsg = { id: uid(), role: "user", content };
    set((st) => ({ chat: [...st.chat, userMsg], chatStreaming: true, agentStarting: true, agentPlan: [] }));
    try {
      const runId = await Agent.Start(content);
      set({ agentRunId: runId, agentStarting: false });
    } catch (e) {
      set((st) => ({
        chat: [...st.chat, { id: uid(), role: "assistant", content: errMessage(e), error: true }],
        chatStreaming: false,
        agentStarting: false,
      }));
    }
  },

  approveAgent: async (callId: string, approved: boolean) => {
    // Optimistically resolve the card (which hides its buttons, preventing a
    // double-submit while the call is in flight).
    set((st) => ({
      chat: st.chat.map((m) => (m.callId === callId ? { ...m, approval: approved ? "approved" : "denied" } : m)),
    }));
    try {
      await Agent.Approve(callId, approved);
    } catch (e) {
      // Roll the card back to pending so the user can retry.
      set((st) => ({
        chat: st.chat.map((m) => (m.callId === callId ? { ...m, approval: "pending" } : m)),
      }));
      get().setStatus(errMessage(e), "error");
    }
  },

  handleAgentEvent: (ev: AgentEvent) => {
    const cur = get().agentRunId;
    if (ev.runId !== cur) {
      // Adopt a run id only for events that arrive before Agent.Start() resolved,
      // and only while WE are actively starting a run (agentStarting). Once any
      // run id is known, foreign ids are ignored — a straggler from a prior run
      // (or an Ask-mode stream) can no longer hijack the current run.
      if (cur === null && get().agentStarting) set({ agentRunId: ev.runId, agentStarting: false });
      else return;
    }
    const match = (m: ChatMsg) => m.callId === ev.callId && m.runId === ev.runId;
    switch (ev.type) {
      case "assistant_text":
        set((st) => ({ chat: [...st.chat, { id: uid(), role: "assistant", content: ev.text ?? "" }] }));
        break;
      case "tool_call":
        set((st) => ({
          chat: [
            ...st.chat,
            { id: uid(), role: "tool", content: "", tool: ev.tool, args: ev.args, callId: ev.callId, runId: ev.runId },
          ],
        }));
        break;
      case "approval_request":
        set((st) => ({ chat: st.chat.map((m) => (match(m) ? { ...m, approval: "pending" } : m)) }));
        break;
      case "continue_request":
        set((st) => ({
          chat: [
            ...st.chat,
            { id: uid(), role: "tool", content: ev.text ?? "", continuePrompt: true, callId: ev.callId, runId: ev.runId, approval: "pending" },
          ],
        }));
        break;
      case "tool_result":
        set((st) => ({
          chat: st.chat.map((m) =>
            match(m) ? { ...m, result: ev.result, approval: m.approval === "pending" ? undefined : m.approval } : m,
          ),
        }));
        break;
      case "plan":
        set({ agentPlan: ev.plan ?? [] });
        break;
      case "done":
        set({ chatStreaming: false, agentRunId: null, agentStarting: false });
        break;
      case "error":
        set((st) => ({
          chat: [...st.chat, { id: uid(), role: "assistant", content: ev.text ?? "Agent error", error: true }],
          chatStreaming: false,
          agentRunId: null,
          agentStarting: false,
        }));
        break;
    }
  },

  sendChat: async (text: string) => {
    const content = text.trim();
    if (!content || get().chatStreaming) return;
    const st = get();
    const userMsg: ChatMsg = { id: uid(), role: "user", content };
    const asstMsg: ChatMsg = { id: uid(), role: "assistant", content: "", streaming: true };
    // Recency window: keep only real user/assistant turns (tool/continue cards
    // and empty streaming placeholders are dropped) and send at most the last
    // MAX_CHAT_HISTORY of them.
    const history = [...st.chat, userMsg]
      .filter((m) => (m.role === "user" || m.role === "assistant") && m.content.trim() !== "")
      .slice(-MAX_CHAT_HISTORY)
      .map((m) => ({ role: m.role, content: m.content }));
    // Functional append so an event that interleaves before this set isn't lost.
    set((s) => ({ chat: [...s.chat, userMsg, asstMsg], chatStreaming: true }));

    // Ground the answer in the active file (Ask mode), capped.
    const active = st.tabs.find((t) => t.path === st.activePath && t.kind === "file");
    const context = active && !active.binary && !active.tooLarge ? active.content.slice(0, 24000) : "";

    try {
      const reqId = await LLM.Send({ messages: history, context, system: "" });
      set({ streamReqId: reqId, streamMsgId: asstMsg.id });
    } catch (e) {
      const msg = errMessage(e);
      set((s) => ({
        chat: s.chat.map((m) => (m.id === asstMsg.id ? { ...m, content: msg, streaming: false, error: true } : m)),
        chatStreaming: false,
      }));
    }
  },

  appendDelta: (reqId: string, delta: string) => {
    const s = get();
    if (reqId !== s.streamReqId || !s.streamMsgId) return;
    set({ chat: s.chat.map((m) => (m.id === s.streamMsgId ? { ...m, content: m.content + delta } : m)) });
  },

  finishStream: (reqId: string) => {
    const s = get();
    if (reqId !== s.streamReqId) return;
    set({
      chat: s.chat.map((m) => (m.id === s.streamMsgId ? { ...m, streaming: false } : m)),
      chatStreaming: false,
      streamReqId: null,
      streamMsgId: null,
    });
  },

  failStream: (reqId: string, message: string) => {
    const s = get();
    if (reqId !== s.streamReqId) return;
    set({
      chat: s.chat.map((m) =>
        m.id === s.streamMsgId
          ? { ...m, content: (m.content ? m.content + "\n\n" : "") + "⚠ " + message, streaming: false, error: true }
          : m,
      ),
      chatStreaming: false,
      streamReqId: null,
      streamMsgId: null,
    });
  },

  cancelChat: () => {
    const s = get();
    if (s.streamReqId) void LLM.Cancel(s.streamReqId);
    if (s.agentRunId) void Agent.Cancel(s.agentRunId);
    set((st) => ({
      chat: st.chat.map((m) => {
        let nm = m;
        if (m.id === st.streamMsgId) nm = { ...nm, streaming: false };
        // A pending approval can never resolve once the run is cancelled — mark
        // it denied so its tool card doesn't sit "Awaiting approval" forever.
        if (nm.approval === "pending") nm = { ...nm, approval: "denied" };
        return nm;
      }),
      chatStreaming: false,
      streamReqId: null,
      streamMsgId: null,
      agentRunId: null,
      agentStarting: false,
    }));
  },

  clearChat: () => {
    // Clearing mid-stream must also stop the in-flight run, or the backend keeps
    // streaming into a void and chatStreaming stays stuck (blocking the next send).
    get().cancelChat();
    set({ chat: [], agentPlan: [] });
  },

  loadModels: async (silent = false) => {
    if (!get().settings?.llm.baseURL) return;
    try {
      const m = await LLM.ListModels();
      set({ models: m });
      // Auto-select a model on first run so chat works without manual setup.
      const cur = get().settings?.llm.model;
      if (!cur && m.length > 0) await get().saveLLMConfig({ model: m[0] });
    } catch (e) {
      if (!silent) get().setStatus(errMessage(e), "error");
    }
  },

  saveLLMConfig: async (patch: Partial<LLMConfig>) => {
    const cur = get().settings;
    if (!cur) return;
    const next = { ...cur, llm: { ...cur.llm, ...patch } };
    if (patch.baseURL !== undefined) next.llm.baseURL = stripUrlCreds(next.llm.baseURL);
    set({ settings: next });
    try {
      await Settings.Save(next);
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  saveEditorConfig: async (patch: Partial<EditorSettings>) => {
    const cur = get().settings;
    if (!cur) return;
    // Clamp numeric prefs so an out-of-range value can't be persisted regardless
    // of how it was entered (the inputs' min/max attributes are only advisory).
    const clamped: Partial<EditorSettings> = { ...patch };
    if (clamped.fontSize !== undefined) clamped.fontSize = Math.max(9, Math.min(28, Math.round(clamped.fontSize)));
    if (clamped.tabSize !== undefined) clamped.tabSize = Math.max(1, Math.min(8, Math.round(clamped.tabSize)));
    const next = { ...cur, editor: { ...cur.editor, ...clamped } };
    set({ settings: next });
    try {
      await Settings.Save(next);
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  setUIFontSize: async (n: number) => {
    const size = Math.max(10, Math.min(20, Math.round(n)));
    const cur = get().settings;
    if (!cur) return;
    const next = { ...cur, uiFontSize: size };
    set({ settings: next });
    applyUIFont(size);
    try {
      await Settings.Save(next);
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  setApiKey: async (value: string) => {
    try {
      if (value.trim()) {
        await SecretService.SetKey(API_KEY_REF, value);
        await get().saveLLMConfig({ apiKeyRef: API_KEY_REF });
        get().setStatus("API key saved", "success");
      } else {
        await SecretService.DeleteKey(API_KEY_REF);
        await get().saveLLMConfig({ apiKeyRef: "" });
      }
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  loadDbProfiles: async () => {
    try {
      const profiles = await Db.ListProfiles();
      set({ dbProfiles: profiles });
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  saveDbProfile: async (p: DbProfile) => {
    try {
      const saved = await Db.SaveProfile(p);
      await get().loadDbProfiles();
      return saved;
    } catch (e) {
      get().setStatus(errMessage(e), "error");
      return null;
    }
  },

  deleteDbProfile: async (id: string) => {
    try {
      await Db.DeleteProfile(id);
      const st = get();
      const tabs = st.tabs.filter((t) => t.path !== `db:${id}`);
      const { [id]: _removed, ...dbSql } = st.dbSql; // drop the dead id's SQL draft
      set({
        tabs,
        dbSql,
        activeDbId: st.activeDbId === id ? null : st.activeDbId,
        dbTables: st.activeDbId === id ? [] : st.dbTables,
        // Clear the loading flag too — otherwise an in-flight selectDb for this id
        // resolves into a now-false activeDbId guard and leaves it stuck true.
        dbTablesLoading: st.activeDbId === id ? false : st.dbTablesLoading,
        // Fall back to the last remaining tab (same rule confirmDelete uses).
        activePath: st.activePath === `db:${id}` ? (tabs[tabs.length - 1]?.path ?? null) : st.activePath,
      });
      await get().loadDbProfiles();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  testDb: async (id: string) => {
    try {
      const res = await Db.TestProfile(id);
      get().setStatus(res.message, res.ok ? "success" : "error");
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  selectDb: async (id: string) => {
    set({ activeDbId: id, dbTables: [], dbTablesLoading: true });
    try {
      const tables = await Db.ListTables(id);
      if (get().activeDbId === id) set({ dbTables: tables, dbTablesLoading: false });
    } catch (e) {
      if (get().activeDbId === id) set({ dbTablesLoading: false });
      get().setStatus(errMessage(e), "error");
    }
  },

  openDbQuery: (id: string) => {
    const key = `db:${id}`;
    const st = get();
    if (!st.tabs.find((t) => t.path === key)) {
      const prof = st.dbProfiles.find((p) => p.id === id);
      const tab: Tab = {
        path: key,
        name: `${prof?.name ?? "Query"} · SQL`,
        kind: "db",
        connId: id,
        language: "sql",
        content: "",
        savedContent: "",
        revision: "",
        binary: false,
        tooLarge: false,
      };
      set({ tabs: [...st.tabs, tab] });
    }
    set({ activePath: key });
  },

  setDbSql: (id: string, sql: string) => set((st) => ({ dbSql: { ...st.dbSql, [id]: sql } })),

  pushDbHistory: (id: string, sql: string) =>
    set((st) => {
      const q = sql.trim();
      if (!q) return {};
      const prev = (st.dbHistory[id] ?? []).filter((s) => s !== q);
      return { dbHistory: { ...st.dbHistory, [id]: [q, ...prev].slice(0, 20) } };
    }),

  openFileMenu: (m: FileMenu) => set({ fileMenu: m }),
  closeFileMenu: () => set({ fileMenu: null }),

  startNewFile: (parentRel: string) =>
    set({ pendingFileOp: { type: "newFile", targetRel: parentRel, initial: "", title: "New File" }, fileMenu: null }),
  startNewFolder: (parentRel: string) =>
    set({ pendingFileOp: { type: "newFolder", targetRel: parentRel, initial: "", title: "New Folder" }, fileMenu: null }),
  startRename: (rel: string, name: string) =>
    set({ pendingFileOp: { type: "rename", targetRel: rel, initial: name, title: "Rename" }, fileMenu: null }),
  cancelFileOp: () => set({ pendingFileOp: null }),

  submitFileOp: async (value: string) => {
    const op = get().pendingFileOp;
    set({ pendingFileOp: null });
    if (!op) return;
    const name = value.trim();
    if (!name) return;
    // Defense in depth (the modal also validates): reject path separators,
    // traversal, and OS-reserved names before they reach the filesystem.
    const invalid = validateFileName(name);
    if (invalid) {
      get().setStatus(invalid, "error");
      return;
    }
    try {
      if (op.type === "rename") {
        const parent = dirOf(op.targetRel);
        const newRel = parent ? `${parent}/${name}` : name;
        if (newRel === op.targetRel) return;
        await Workspace.Rename(op.targetRel, newRel);
        set((st) => {
          const activeIdx = st.tabs.findIndex((t) => t.path === st.activePath);
          const tabs = st.tabs.map((t) => remapTab(t, op.targetRel, newRel));
          return {
            tabs,
            activePath: activeIdx >= 0 ? tabs[activeIdx].path : st.activePath,
            selectedPath: newRel,
          };
        });
        await get().loadChildren(parent);
      } else {
        const rel = op.targetRel ? `${op.targetRel}/${name}` : name;
        if (op.type === "newFile") {
          await Workspace.CreateFile(rel);
        } else {
          await Workspace.CreateDir(rel);
        }
        set((st) => ({ expanded: { ...st.expanded, [op.targetRel]: true } }));
        await get().loadChildren(op.targetRel);
        if (op.type === "newFile") await get().openFile(rel, name);
      }
      void get().loadGitStatus();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
      // The op may have failed because the entry was already moved/removed
      // outside Novera — reconcile the affected directory so the tree isn't left
      // showing a stale entry.
      void get().loadChildren(op.type === "rename" ? dirOf(op.targetRel) : op.targetRel);
    }
  },

  requestDelete: (rel: string, name: string) => set({ pendingDelete: { rel, name }, fileMenu: null }),
  cancelDelete: () => set({ pendingDelete: null }),
  confirmDelete: async () => {
    const pd = get().pendingDelete;
    set({ pendingDelete: null });
    if (!pd) return;
    try {
      await Workspace.Delete(pd.rel);
      const parent = dirOf(pd.rel);
      set((st) => {
        const tabs = st.tabs.filter((t) => !(t.path === pd.rel || t.path.startsWith(pd.rel + "/")));
        const stillActive =
          st.activePath && (st.activePath === pd.rel || st.activePath.startsWith(pd.rel + "/"));
        return {
          tabs,
          activePath: stillActive ? (tabs[tabs.length - 1]?.path ?? null) : st.activePath,
        };
      });
      await get().loadChildren(parent);
      void get().loadGitStatus();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
      // Reconcile in case the entry was already gone (stale tree node).
      void get().loadChildren(dirOf(pd.rel));
    }
  },

  setSearchQuery: (query: string) =>
    set(query.trim() ? { searchQuery: query } : { searchQuery: query, searchResults: null, searching: false }),

  runSearch: async (query: string) => {
    set({ searchQuery: query });
    const q = query.trim();
    if (!q) {
      set({ searchResults: null });
      return;
    }
    set({ searching: true });
    try {
      const r = await Workspace.Search(q, false);
      // Stale-response guard: only commit if this is still the current query.
      if (get().searchQuery === query) set({ searchResults: r });
    } catch (e) {
      if (get().searchQuery === query) get().setStatus(errMessage(e), "error");
    } finally {
      if (get().searchQuery === query) set({ searching: false });
    }
  },

  openFileAt: async (path: string, line: number, column: number) => {
    await get().openFile(path, baseName(path));
    // Line/column reveal only works in the Monaco editor. For tabular, binary, or
    // too-large targets there's no editor to reveal in — open them but tell the
    // user rather than silently dropping the navigation.
    const tab = get().tabs.find((t) => t.path === path);
    const navigable = tab?.kind === "file" && !tab.binary && !tab.tooLarge;
    if (!navigable) {
      get().setStatus("Opened — line navigation isn't available for this file type.", "info");
      return;
    }
    set({ pendingReveal: { path, line, column } });
  },

  clearReveal: () => set({ pendingReveal: null }),

  setView: (v: ViewId) => set({ view: v }),
  toggleSidebar: () => set((st) => ({ sidebarVisible: !st.sidebarVisible })),
  togglePanel: () => set((st) => ({ panelVisible: !st.panelVisible, panelMounted: st.panelMounted || !st.panelVisible })),

  openPalette: (mode) => {
    set({ paletteOpen: true, paletteMode: mode });
    if (mode === "files" && get().allFiles.length === 0) void get().loadAllFiles();
  },
  closePalette: () => set({ paletteOpen: false }),

  openAuditLog: async () => {
    set({ auditOpen: true, auditLoading: true });
    try {
      const entries = await Agent.AuditLog(500);
      set({ auditEntries: entries, auditLoading: false });
    } catch (e) {
      set({ auditLoading: false });
      get().setStatus(errMessage(e), "error");
    }
  },
  closeAuditLog: () => set({ auditOpen: false }),

  setPanelTab: (tab) => set({ panelTab: tab }),

  loadJobs: async () => {
    try {
      set({ jobs: await Jobs.ListJobs() });
    } catch {
      /* ledger unavailable — leave jobs as-is */
    }
  },
  cancelJob: async (id: string) => {
    try {
      await Jobs.CancelJob(id);
      await get().loadJobs();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },
  clearFinishedJobs: async () => {
    try {
      await Jobs.ClearFinished();
      await get().loadJobs();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },

  loadArtifacts: async () => {
    try {
      set({ artifacts: await Artifacts.ListArtifacts() });
    } catch {
      /* no workspace / unavailable — leave as-is */
    }
  },
  setArtifactArchived: async (id: string, archived: boolean) => {
    try {
      await Artifacts.SetArchived(id, archived);
      await get().loadArtifacts();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },
  deleteArtifact: async (id: string) => {
    try {
      await Artifacts.DeleteArtifact(id);
      await get().loadArtifacts();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },
  // Register the active editor file as an artifact (manual, user-driven producer).
  saveActiveAsArtifact: async (kind: string) => {
    const tab = get().tabs.find((t) => t.path === get().activePath);
    if (!tab || tab.kind !== "file") {
      get().setStatus("Open a file to save it as an artifact", "error");
      return;
    }
    try {
      await Artifacts.CreateArtifact({
        id: "",
        kind,
        title: tab.name,
        path: tab.path,
        tool: "manual",
        note: "",
        sources: [],
        createdAt: 0,
        updatedAt: 0,
        archived: false,
        stale: false,
        missing: false,
      });
      await get().loadArtifacts();
      get().setStatus(`Saved ${tab.name} as a ${kind} artifact`, "success");
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    }
  },
  runDiagnostics: async () => {
    if (!get().isOpen) {
      set({ diagnostics: null });
      return;
    }
    set({ loadingDiagnostics: true });
    try {
      const d = await Workspace.Diagnostics();
      set({ diagnostics: d });
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      set({ loadingDiagnostics: false });
    }
  },
  showProblems: () => {
    set({ panelVisible: true, panelMounted: true, panelTab: "problems" });
    if (!get().diagnostics) void get().runDiagnostics();
  },
  resizeSidebar: (delta) => set((st) => ({ sidebarWidth: clamp(st.sidebarWidth + delta, 180, 640) })),
  resizeAssistant: (delta) => set((st) => ({ assistantWidth: clamp(st.assistantWidth - delta, 260, 720) })),
  resizePanel: (delta) => set((st) => ({ panelHeight: clamp(st.panelHeight - delta, 120, 640) })),
  loadAllFiles: async () => {
    if (!get().isOpen) {
      set({ allFiles: [], loadingAllFiles: false, allFilesError: null });
      return;
    }
    set({ loadingAllFiles: true, allFilesError: null });
    try {
      const files = await Workspace.ListAllFiles();
      set({ allFiles: files, loadingAllFiles: false });
    } catch (e) {
      set({ loadingAllFiles: false, allFilesError: errMessage(e) });
      get().setStatus(errMessage(e), "error");
    }
  },

  setStatus: (message: string, kind: StatusKind = "info") => {
    set({ status: { message, kind } });
    if (statusTimer) clearTimeout(statusTimer);
    // Info/success are transient; errors persist until replaced or dismissed so
    // a failure can't flash by before the user reads it.
    if (kind !== "error") {
      statusTimer = setTimeout(() => set({ status: null }), 4000);
    }
  },

  dismissStatus: () => {
    if (statusTimer) clearTimeout(statusTimer);
    set({ status: null });
  },
}));
