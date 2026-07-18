import { create } from "zustand";
import {
  Workspace,
  Settings,
  Git,
  LLM,
  Agent,
  Db,
  Watcher,
  Jobs,
  Artifacts,
  BigFile,
  SecretService,
  Shell,
  errMessage,
  STALE_MARKER,
} from "../lib/services";
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
import type { DbCredentialStatus } from "../lib/dbCredential";
import { languageForPath, isTabular } from "../lib/lang";
import { activeResourceCapabilities } from "../lib/resourceCapabilities";
import { validateFileName } from "../lib/validate";
import { ExclusiveOperation } from "../lib/exclusiveOperation";

// Result of a Tools-menu utility, shown in the ToolsModal.
export type ToolResult =
  | { kind: "schema"; title: string; rel: string; schema: SchemaResult }
  | { kind: "dump"; title: string; rel: string; dump: DumpSummary }
  | { kind: "sql"; title: string; rel: string; table: string; out: string; sql: string }
  | { kind: "error"; title: string; message: string };

export type ViewId = "explorer" | "search" | "git" | "db" | "artifacts" | "settings";
export type StatusKind = "info" | "error" | "success";
export type TabKind = "file" | "diff" | "db" | "table";

// A save caller must be able to distinguish a confirmed write from a handled
// error. In particular, close flows may only discard the in-memory tab after a
// successful write of the snapshot they asked to save.
export type TabSaveResult =
  | { ok: true; outcome: "saved" | "unchanged"; submittedContent: string }
  | { ok: false; outcome: "failed" | "superseded" | "unavailable"; submittedContent?: string; message?: string };

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
  intent?: string;
  intentDigest?: string;
  expiresAt?: string;
  // continuePrompt messages are the "keep going at a step checkpoint?" cards
  // (content holds the step count); they reuse the approval flow.
  continuePrompt?: boolean;
}

export type PlanStatus = "todo" | "in_progress" | "done";
export interface PlanStep {
  title: string;
  status: PlanStatus;
}

function settleAgentPlan(plan: PlanStep[]): PlanStep[] {
  return plan.map((step) => (step.status === "in_progress" ? { ...step, status: "todo" } : step));
}

function settlePendingAgentCards(chat: ChatMsg[], runId: string): ChatMsg[] {
  return chat.map((message) =>
    message.runId === runId && message.approval === "pending" ? { ...message, approval: "denied" } : message,
  );
}

export interface AgentEvent {
  runId: string;
  seq?: number;
  type: string;
  canceled?: boolean;
  rolledBack?: boolean;
  text?: string;
  callId?: string;
  tool?: string;
  args?: string;
  intent?: string;
  intentDigest?: string;
  expiresAt?: string;
  result?: string;
  plan?: PlanStep[];
}

interface AgentEventQueue {
  nextSeq: number;
  pending: Map<number, AgentEvent>;
}

// Wails handlers may run independently, so delivery order is not a valid run
// order. Keep early events by the backend-issued run id and release only a
// contiguous sequence after Agent.Start confirms which run belongs to this UI
// attempt. Unknown runs are never adopted from an event.
const agentEventQueues = new Map<string, AgentEventQueue>();
const agentEventQueueFailures = new Map<string, string>();
const MAX_EARLY_EVENT_IDS = 8;
const MAX_AGENT_PENDING_EVENTS = 512;
const MAX_STREAM_PENDING_EVENTS = 4096;

function evictOldestEventID<T>(queues: Map<string, T>, failures: Map<string, string>): void {
  const oldest = queues.keys().next().value as string | undefined;
  if (oldest !== undefined) {
    queues.delete(oldest);
    failures.delete(oldest);
  }
}

function bufferAgentEvent(ev: AgentEvent): void {
  let queue = agentEventQueues.get(ev.runId);
  if (!queue) {
    if (agentEventQueues.size >= MAX_EARLY_EVENT_IDS) evictOldestEventID(agentEventQueues, agentEventQueueFailures);
    queue = { nextSeq: 1, pending: new Map() };
    agentEventQueues.set(ev.runId, queue);
  }
  if (!Number.isSafeInteger(ev.seq) || ev.seq === undefined || ev.seq <= 0) {
    agentEventQueueFailures.set(ev.runId, "Agent event was missing a valid backend sequence number; the run was stopped safely.");
    return;
  }
  if (ev.seq < queue.nextSeq || queue.pending.has(ev.seq)) return;
  if (ev.seq - queue.nextSeq >= MAX_AGENT_PENDING_EVENTS || queue.pending.size >= MAX_AGENT_PENDING_EVENTS) {
    queue.pending.clear();
    agentEventQueueFailures.set(ev.runId, "Agent event ordering exceeded the safe buffer limit; the run was stopped safely.");
    return;
  }
  queue.pending.set(ev.seq, ev);
}

function takeReadyAgentEvents(runId: string): AgentEvent[] {
  const queue = agentEventQueues.get(runId);
  if (!queue) return [];
  const ready: AgentEvent[] = [];
  while (queue.pending.has(queue.nextSeq)) {
    ready.push(queue.pending.get(queue.nextSeq)!);
    queue.pending.delete(queue.nextSeq);
    queue.nextSeq++;
  }
  return ready;
}

type PendingStreamEvent =
  | { type: "delta"; delta: string; seq?: number }
  | { type: "done"; seq?: number }
  | { type: "error"; message: string; seq?: number };

interface StreamEventQueue {
  nextSeq: number;
  pending: Map<number, PendingStreamEvent>;
}

const streamEventQueues = new Map<string, StreamEventQueue>();
const streamEventQueueFailures = new Map<string, string>();

function bufferStreamEvent(reqId: string, event: PendingStreamEvent): void {
  let queue = streamEventQueues.get(reqId);
  if (!queue) {
    if (streamEventQueues.size >= MAX_EARLY_EVENT_IDS) evictOldestEventID(streamEventQueues, streamEventQueueFailures);
    queue = { nextSeq: 1, pending: new Map() };
    streamEventQueues.set(reqId, queue);
  }
  if (!Number.isSafeInteger(event.seq) || event.seq === undefined || event.seq <= 0) {
    streamEventQueueFailures.set(reqId, "Assistant stream event was missing a valid backend sequence number; the request was stopped safely.");
    return;
  }
  if (event.seq < queue.nextSeq || queue.pending.has(event.seq)) return;
  if (event.seq - queue.nextSeq >= MAX_STREAM_PENDING_EVENTS || queue.pending.size >= MAX_STREAM_PENDING_EVENTS) {
    queue.pending.clear();
    streamEventQueueFailures.set(reqId, "Assistant stream ordering exceeded the safe buffer limit; the request was stopped safely.");
    return;
  }
  queue.pending.set(event.seq, event);
}

function takeReadyStreamEvents(reqId: string): PendingStreamEvent[] {
  const queue = streamEventQueues.get(reqId);
  if (!queue) return [];
  const ready: PendingStreamEvent[] = [];
  while (queue.pending.has(queue.nextSeq)) {
    ready.push(queue.pending.get(queue.nextSeq)!);
    queue.pending.delete(queue.nextSeq);
    queue.nextSeq++;
  }
  return ready;
}

export interface LLMConfig {
  provider: string;
  baseURL: string;
  model: string;
  apiKeyRef: string;
  requestTimeoutSec: number;
}

export interface AgentConfig {
  maxToolOutputChars: number;
  stepBatch: number;
  maxTotalSteps: number;
  historyWindowGroups: number;
  commandTimeoutSec: number;
}

let uidCounter = 0;
const uid = () => `m${++uidCounter}`;
let saveRequestTokenCounter = 0;
let closeAttemptTokenCounter = 0;
let tabInstanceIdCounter = 0;
let workspaceInstanceIdCounter = 0;
let modelLoadAttemptCounter = 0;
const nextSaveRequestToken = () => ++saveRequestTokenCounter;
const nextCloseAttemptToken = () => ++closeAttemptTokenCounter;
const nextTabInstanceId = () => ++tabInstanceIdCounter;
const nextWorkspaceInstanceId = () => ++workspaceInstanceIdCounter;

// Workspace.Open/Close mutate one process-global backend root. Keep those
// transitions ordered even when several UI intents overlap, and let queued
// intents check whether they are still the latest before touching the backend.
// This complements workspaceInstanceId, which protects renderer-side commits.
let workspaceTransitionQueue: Promise<void> = Promise.resolve();

function enqueueWorkspaceTransition<T>(operation: () => Promise<T>): Promise<T> {
  const result = workspaceTransitionQueue.then(operation, operation);
  workspaceTransitionQueue = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

let watchSyncAttemptCounter = 0;
let latestWatchSyncAttempt = 0;
let watchOperationQueue: Promise<void> = Promise.resolve();

function enqueueWatchOperation<T>(operation: () => Promise<T>): Promise<T> {
  const result = watchOperationQueue.then(operation, operation);
  watchOperationQueue = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

// Explorer mutations all target the same process-global workspace tree. Keep
// them ordered, reject queued intents whose file-tree generation is stale, and
// expose their affected paths to editor saves so a rename/delete cannot race a
// write to the old path.
let fileOperationQueue: Promise<void> = Promise.resolve();
let workspaceFileStateGeneration = 0;
let fileMutationHistoryFloor = 0;
const fileMutationHistory: { generation: number; oldRel: string; newRel?: string }[] = [];
const activeFileMutationPaths = new Map<string, { workspaceInstanceId: number; rel: string; refs: number }>();
let directoryLoadAttemptCounter = 0;
const latestDirectoryLoadAttempts = new Map<string, number>();
const toolOperation = new ExclusiveOperation();

function enqueueFileOperation<T>(operation: () => Promise<T>): Promise<T> {
  const result = fileOperationQueue.then(operation, operation);
  fileOperationQueue = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

function recordFileStateMutation(oldRel: string, newRel?: string): number {
  const generation = ++workspaceFileStateGeneration;
  fileMutationHistory.push({ generation, oldRel, newRel });
  if (fileMutationHistory.length > 256) {
    fileMutationHistoryFloor = fileMutationHistory.shift()!.generation;
  }
  return generation;
}

function pathChangedSince(generation: number, path: string): boolean {
  if (generation < fileMutationHistoryFloor) return true;
  return fileMutationHistory.some(
    (mutation) =>
      mutation.generation > generation &&
      (pathAtOrBelow(path, mutation.oldRel) || (!!mutation.newRel && pathAtOrBelow(path, mutation.newRel))),
  );
}

function pathAtOrBelow(path: string, rel: string): boolean {
  return path === rel || path.startsWith(rel + "/");
}

function fileMutationKey(workspaceInstanceId: number, rel: string): string {
  return `${workspaceInstanceId}\0${rel}`;
}

function claimFileMutationPath(workspaceInstanceId: number, rel: string): () => void {
  const key = fileMutationKey(workspaceInstanceId, rel);
  const existing = activeFileMutationPaths.get(key);
  if (existing) existing.refs++;
  else activeFileMutationPaths.set(key, { workspaceInstanceId, rel, refs: 1 });
  return () => {
    const current = activeFileMutationPaths.get(key);
    if (!current) return;
    if (--current.refs === 0) activeFileMutationPaths.delete(key);
  };
}

function hasActiveFileMutation(workspaceInstanceId: number, path: string): boolean {
  for (const mutation of activeFileMutationPaths.values()) {
    if (mutation.workspaceInstanceId === workspaceInstanceId && pathAtOrBelow(path, mutation.rel)) return true;
  }
  return false;
}

function claimDirectoryLoad(workspaceInstanceId: number, path: string): { key: string; attempt: number } {
  const key = `${workspaceInstanceId}\0${path}`;
  const attempt = ++directoryLoadAttemptCounter;
  latestDirectoryLoadAttempts.set(key, attempt);
  return { key, attempt };
}

function invalidateDirectoryLoads(workspaceInstanceId: number, rel: string): void {
  const prefix = `${workspaceInstanceId}\0`;
  for (const key of latestDirectoryLoadAttempts.keys()) {
    if (key.startsWith(prefix) && pathAtOrBelow(key.slice(prefix.length), rel)) {
      latestDirectoryLoadAttempts.delete(key);
    }
  }
}

// Wails cancellation calls are promises in production, but keeping the call
// itself inside try/catch also makes synchronous bridge/version-skew failures
// observable and keeps test doubles from creating unhandled rejections.
function invokeBestEffort(operation: () => unknown, onError: (error: unknown) => void): void {
  try {
    void Promise.resolve(operation()).catch(onError);
  } catch (error) {
    onError(error);
  }
}

// Git.Status and index mutations all observe one process-global repository
// root. Keep them in a single renderer queue so an older status read can never
// land after a newer mutation result. Workspace identity checks at execution
// and commit time prevent queued work from crossing a root transition.
let gitOperationQueue: Promise<void> = Promise.resolve();
let gitOperationAttemptCounter = 0;
let latestGitOperationAttempt = 0;

function claimGitOperation(): number {
  latestGitOperationAttempt = ++gitOperationAttemptCounter;
  return latestGitOperationAttempt;
}

function enqueueGitOperation<T>(operation: () => Promise<T>): Promise<T> {
  const result = gitOperationQueue.then(operation, operation);
  gitOperationQueue = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

interface GitMutationOutcome<T> {
  result?: T;
  mutationError?: unknown;
  status?: GitStatus;
  refreshError?: unknown;
}

async function mutateAndRefreshGit<T>(
  mutation: () => Promise<T>,
  fallbackStatus: (result: T) => GitStatus | null,
): Promise<GitMutationOutcome<T>> {
  const outcome: GitMutationOutcome<T> = {};
  let mutationSucceeded = false;
  try {
    outcome.result = await mutation();
    mutationSucceeded = true;
  } catch (error) {
    outcome.mutationError = error;
  }

  // A bridge rejection is ambiguous: the backend may have completed the index
  // change before delivery failed. Always re-read the repository, on success
  // and failure alike, before rendering another actionable snapshot.
  try {
    outcome.status = await Git.Status();
  } catch (error) {
    outcome.refreshError = error;
    if (mutationSucceeded) outcome.status = fallbackStatus(outcome.result as T) ?? undefined;
  }
  return outcome;
}

// Settings are persisted as one object, while several independent controls and
// RememberWorkspace can mutate it. Keep all renderer-originated settings
// mutations in one queue so out-of-order bridge dispatch cannot let an older
// whole-object save overwrite a newer one.
let settingsMutationQueue: Promise<void> = Promise.resolve();
let settingsMutationCounter = 0;
let latestSettingsMutation = 0;

function claimSettingsMutation(): number {
  latestSettingsMutation = ++settingsMutationCounter;
  return latestSettingsMutation;
}

function enqueueSettingsMutation<T>(operation: () => Promise<T>): Promise<T> {
  const result = settingsMutationQueue.then(operation, operation);
  settingsMutationQueue = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

interface AuthoritativeSettingsState {
  settings: SettingsModel;
  settingsError: string | null;
  llmAPIKeyStatus: LLMCredentialState;
  llmAPIKeyStatusMessage: string;
  llmAPIKeyAvailable: boolean;
}

export type LLMCredentialState = "missing" | "verified" | "quarantined" | "unavailable";

function isLLMCredentialState(value: unknown): value is LLMCredentialState {
  return value === "missing" || value === "verified" || value === "quarantined" || value === "unavailable";
}

async function loadAuthoritativeSettingsState(): Promise<AuthoritativeSettingsState> {
  const settings = await Settings.Load();
  let settingsError: string | null = null;
  let llmAPIKeyStatus: LLMCredentialState = "unavailable";
  let llmAPIKeyStatusMessage = "Credential storage status could not be verified.";
  try {
    settingsError = (await Settings.LoadError()).trim() || null;
  } catch {
    // Keep the successfully loaded settings usable if only the diagnostic call
    // is unavailable (for example, during a generated-binding version skew).
  }
  if (settingsError) {
    llmAPIKeyStatusMessage = settingsError;
  } else {
    try {
      const status = await Settings.GetLLMAPIKeyStatus();
      if (isLLMCredentialState(status.state)) {
        llmAPIKeyStatus = status.state;
        llmAPIKeyStatusMessage = typeof status.message === "string" ? status.message : "";
      }
    } catch {
      // Compatibility fallback for an older generated/backend binding. It
      // remains fail-closed and never upgrades opaque ref presence to verified.
      try {
        if (await Settings.HasLLMAPIKey()) {
          llmAPIKeyStatus = "verified";
          llmAPIKeyStatusMessage = "";
        } else if (settings.llm.apiKeyRef) {
          llmAPIKeyStatus = "quarantined";
          llmAPIKeyStatusMessage = "The stored credential is not usable for this provider origin; re-entry may be required.";
        } else {
          llmAPIKeyStatus = "missing";
          llmAPIKeyStatusMessage = "";
        }
      } catch {
        // Keep the unavailable state and diagnostic above.
      }
    }
  }
  return {
    settings,
    settingsError,
    llmAPIKeyStatus,
    llmAPIKeyStatusMessage,
    llmAPIKeyAvailable: llmAPIKeyStatus === "verified",
  };
}

// Database profile creates and deletes share one persisted array. Serialize UI
// mutations so a slow bridge response cannot make an older reconciliation
// overwrite a newer one in renderer state.
let dbMutationQueue: Promise<void> = Promise.resolve();
const inFlightDbCreates = new WeakMap<DbProfile, Promise<DbProfile | null>>();
let dbSelectionGeneration = 0;
let dbPreviewRequestCounter = 0;
let latestDbPreviewRequest = 0;

function enqueueDbMutation<T>(operation: () => Promise<T>): Promise<T> {
  const result = dbMutationQueue.then(operation, operation);
  dbMutationQueue = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

interface AuthoritativeDatabaseState {
  profiles: DbProfile[];
  credentialStatuses: Record<string, DbCredentialStatus>;
  loadError: string | null;
  credentialStatusError: string | null;
}

async function loadAuthoritativeDatabaseState(): Promise<AuthoritativeDatabaseState> {
  const profiles = await Db.ListProfiles();
  let loadError: string | null = null;
  let credentialStatusError: string | null = null;
  const credentialStatuses: Record<string, DbCredentialStatus> = {};
  try {
    loadError = (await Db.LoadError()).trim() || null;
  } catch {
    // Without the backend's latched safety diagnostic, allowing mutations could
    // overwrite preserved evidence. Keep the renderer fail-closed.
    loadError = "Database profile safety status is unavailable. Profile changes are disabled until it can be verified.";
  }
  try {
    const profilesByID = new Map(profiles.map((profile) => [profile.id, profile]));
    for (const entry of await Db.CredentialStatuses()) {
      const profile = profilesByID.get(entry.profileId);
      // ListProfiles and CredentialStatuses are separate bridge calls. Correlate
      // the backend-owned handle so an external concurrent scope edit can only
      // make the renderer fail closed, never apply a verified state to stale
      // profile metadata with the same ID.
      if (profile && profile.secretRef.trim() === entry.secretRef.trim() && ["none", "verified", "quarantined", "unavailable"].includes(entry.status)) {
        credentialStatuses[entry.profileId] = entry.status as DbCredentialStatus;
      }
    }
  } catch (error) {
    // Missing entries are intentionally rendered as unavailable, never stored.
    credentialStatusError = `Database credential status is unavailable: ${errMessage(error)}`;
  }
  return { profiles, credentialStatuses, loadError, credentialStatusError };
}

// Serialize ordinary saves and encoding conversions through one queue per
// relative path. Besides complementing the backend's write lock, this lets a
// later save capture the revision returned by its predecessor instead of
// starting from an already-obsolete editor snapshot.
const tabSaveQueues = new Map<string, Promise<void>>();

async function waitForTabSaves(workspaceInstanceId: number, rel: string): Promise<void> {
  const prefix = `${workspaceInstanceId}\0`;
  const pending: Promise<void>[] = [];
  for (const [key, tail] of tabSaveQueues) {
    if (key.startsWith(prefix) && pathAtOrBelow(key.slice(prefix.length), rel)) pending.push(tail);
  }
  if (pending.length) await Promise.all(pending);
}

function enqueueTabSave<T>(workspaceInstanceId: number, path: string, operation: () => Promise<T>): Promise<T> {
  const queueKey = `${workspaceInstanceId}\0${path}`;
  const preceding = tabSaveQueues.get(queueKey);
  // Preserve ordinary save-at-click semantics for an idle path; only requests
  // that actually have a predecessor defer their snapshot capture.
  const result = preceding ? preceding.then(operation) : operation();
  // Queue tails never reject, so one unexpected failure cannot strand later
  // requests for the path. The caller still receives the original result.
  const tail = result.then(
    () => undefined,
    () => undefined,
  );
  tabSaveQueues.set(queueKey, tail);
  void tail.then(() => {
    if (tabSaveQueues.get(queueKey) === tail) tabSaveQueues.delete(queueKey);
  });
  return result;
}

const API_KEY_REF = "llm.apikey";
// Ask-mode chat: cap how many recent user/assistant turns are re-sent each
// message so a long conversation can't blow past the model's context window.
const MAX_CHAT_HISTORY = 20;
export const DEFAULT_OLLAMA_MODELS = ["gemma4:12b-it-q8_0", "gemma4:12b"];
export const DEFAULT_AGENT_CONFIG: AgentConfig = {
  maxToolOutputChars: 6000,
  stepBatch: 50,
  maxTotalSteps: 1000,
  historyWindowGroups: 8,
  commandTimeoutSec: 60,
};

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

export function agentConfigFromSettings(settings: SettingsModel | null): AgentConfig {
  const agent = (settings as (SettingsModel & { agent?: Partial<AgentConfig> }) | null)?.agent ?? {};
  return { ...DEFAULT_AGENT_CONFIG, ...agent };
}

function clampAgentConfig(patch: Partial<AgentConfig>, current: AgentConfig): AgentConfig {
  const next = { ...current, ...patch };
  next.maxToolOutputChars = Math.max(1000, Math.min(50000, Math.round(next.maxToolOutputChars || DEFAULT_AGENT_CONFIG.maxToolOutputChars)));
  next.stepBatch = Math.max(1, Math.min(500, Math.round(next.stepBatch || DEFAULT_AGENT_CONFIG.stepBatch)));
  next.maxTotalSteps = Math.max(next.stepBatch, Math.min(10000, Math.round(next.maxTotalSteps || DEFAULT_AGENT_CONFIG.maxTotalSteps)));
  next.historyWindowGroups = Math.max(1, Math.min(50, Math.round(next.historyWindowGroups || DEFAULT_AGENT_CONFIG.historyWindowGroups)));
  next.commandTimeoutSec = Math.max(5, Math.min(3600, Math.round(next.commandTimeoutSec || DEFAULT_AGENT_CONFIG.commandTimeoutSec)));
  return next;
}

function mergeModels(preferred: string[], discovered: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const model of [...preferred, ...discovered]) {
    const trimmed = model.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    out.push(trimmed);
  }
  return out;
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

function tabResourcePath(tab: Tab): string | null {
  if (tab.kind === "file") return tab.path;
  if (tab.kind === "table" || tab.kind === "diff") return tab.rel ?? null;
  return null;
}

// Remap any workspace-backed tab kind when a file or its parent folder is
// renamed. Recompute every path-derived field together: the Monaco model key,
// label, language, table rel, and staged/unstaged diff key must never disagree.
function remapTab(t: Tab, oldRel: string, newRel: string): Tab {
  if (t.kind === "file") {
    const path = remapPath(t.path, oldRel, newRel);
    return path === t.path ? t : { ...t, path, name: baseName(path), language: languageForPath(path) };
  }
  if (t.kind === "table" && t.rel) {
    const rel = remapPath(t.rel, oldRel, newRel);
    return rel === t.rel ? t : { ...t, rel, path: rel, name: baseName(rel) };
  }
  if (t.kind === "diff" && t.rel) {
    const rel = remapPath(t.rel, oldRel, newRel);
    if (rel === t.rel) return t;
    // Preserve the staged/unstaged discriminator embedded in the tab key.
    const staged = t.path.startsWith("diff:s:");
    const prefix = staged ? "diff:s:" : "diff:u:";
    return {
      ...t,
      rel,
      path: `${prefix}${rel}`,
      name: `${baseName(rel)} ${staged ? "(staged)" : "(working tree)"}`,
      language: languageForPath(rel),
    };
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
  instanceId: number; // stable identity across edits/reloads; never reused for a reopened tab
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
	largeFileSessionId?: string;
	largeFileDirty?: boolean;
	largeFileInPlaceEligible?: boolean;
  encoding?: string; // detected on-disk encoding (utf-8, utf-16le, latin-1, …); save round-trips it
  staleOnDisk?: boolean; // changed on disk by another program
  staleRevision?: string; // exact external revision observed for explicit conflict overwrite
  sourceVersion?: number; // invalidates non-text views after a watcher event
  saveRequestToken?: number; // globally unique identity of the latest save started for this tab instance
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
  workspaceInstanceId: number;
  workspaceTransitioning: boolean;
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
  streamAttempt: number;
  agentRunId: string | null;
  agentStarting: boolean; // true between Agent.Start dispatch and the run's first event
  agentAttempt: number;
  agentPlan: PlanStep[];
  conversationResetting: boolean;
  conversationResetAttempt: number;

  // database
  dbProfiles: DbProfile[];
  dbCredentialStatuses: Record<string, DbCredentialStatus>;
  dbError: string | null;
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
  pendingTabCloseAttemptToken: number | null;
  pendingTabCloseSaving: boolean;

  // Tools menu (CSV/dump utilities) result shown in a modal
  toolResult: ToolResult | null;
  toolBusy: boolean;
  cleanDump: { rel: string } | null; // active "Clean SQL dump" form target
  dataTools: { rel: string } | null; // active "Data tools" (big-file CSV/SQL toolset) target
  aboutOpen: boolean; // Help → About Novera dialog

  // ui
  view: ViewId;
  sidebarVisible: boolean;
  panelVisible: boolean;
  panelMounted: boolean; // stays true after first open so the terminal PTY survives toggles
  panelTab: "terminal" | "problems" | "jobs";
  jobs: Job[];
  artifacts: Artifact[];
  artifactsError: string | null;
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
  settingsError: string | null;
  llmAPIKeyStatus: LLMCredentialState;
  llmAPIKeyStatusMessage: string;
  llmAPIKeyAvailable: boolean;
  appReady: boolean; // backend init() has finished — used to dismiss the boot splash

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
	setLargeFileState: (
		path: string,
		state: { sessionId?: string; dirty?: boolean; inPlaceEligible?: boolean },
	) => void;
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
  openDataTools: () => void;
  closeDataTools: () => void;
  openAbout: () => void;
  closeAbout: () => void;
  applyCleanDump: (rel: string, outRel: string, t: DumpTransform) => Promise<void>;
  cancelCleanDump: () => void;
  closeToolResult: () => void;
  updateContent: (path: string, content: string) => void;
  saveActive: () => Promise<void>;
  saveTab: (path: string) => Promise<TabSaveResult>;
  overwriteStaleTab: (path: string) => Promise<TabSaveResult>;
  convertEncoding: (path: string, encoding: string) => Promise<TabSaveResult>;
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
  approveAgent: (callId: string, approved: boolean, intentDigest?: string) => Promise<void>;
  handleAgentEvent: (ev: AgentEvent) => void;
  flushAgentEvents: (runId: string) => void;
  cancelChat: () => void;
  clearChat: () => void;
  appendDelta: (reqId: string, delta: string, seq?: number) => void;
  finishStream: (reqId: string, seq?: number) => void;
  failStream: (reqId: string, message: string, seq?: number) => void;
  flushStreamEvents: (reqId: string) => void;
  loadModels: (silent?: boolean) => Promise<void>;
  saveLLMConfig: (patch: Partial<LLMConfig>) => Promise<void>;
  saveAgentConfig: (patch: Partial<AgentConfig>) => Promise<void>;
  saveEditorConfig: (patch: Partial<EditorSettings>) => Promise<void>;
  setUIFontSize: (n: number) => Promise<void>;
  setApiKey: (value: string) => Promise<boolean>;

  // actions — database
  loadDbProfiles: () => Promise<void>;
  saveDbProfile: (p: DbProfile) => Promise<DbProfile | null>;
  deleteDbProfile: (id: string) => Promise<void>;
  testDb: (id: string) => Promise<void>;
  selectDb: (id: string) => Promise<void>;
  openDbTable: (id: string, table: DbTable) => Promise<void>;
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

function remapRecordKeys<T>(record: Record<string, T>, oldRel: string, newRel: string): Record<string, T> {
  const next: Record<string, T> = {};
  for (const [path, value] of Object.entries(record)) next[remapPath(path, oldRel, newRel)] = value;
  return next;
}

function removeRecordPathKeys<T>(record: Record<string, T>, rel: string): Record<string, T> {
  return Object.fromEntries(Object.entries(record).filter(([path]) => !pathAtOrBelow(path, rel)));
}

function remapEntry(entry: Entry, oldRel: string, newRel: string): Entry {
  const path = remapPath(entry.path, oldRel, newRel);
  return path === entry.path ? entry : { ...entry, path, name: baseName(path) };
}

function remapToolResult(result: ToolResult | null, oldRel: string, newRel: string): ToolResult | null {
  if (!result || result.kind === "error") return result;
  const rel = remapPath(result.rel, oldRel, newRel);
  if (rel === result.rel && (result.kind !== "sql" || remapPath(result.out, oldRel, newRel) === result.out)) return result;
  const oldName = baseName(result.rel);
  const title = oldName === baseName(rel) ? result.title : result.title.replace(oldName, baseName(rel));
  if (result.kind === "sql") return { ...result, title, rel, out: remapPath(result.out, oldRel, newRel) };
  return { ...result, title, rel };
}

function deleteToolResult(result: ToolResult | null, rel: string): ToolResult | null {
  if (!result || result.kind === "error") return result;
  if (pathAtOrBelow(result.rel, rel)) return null;
  if (result.kind === "sql" && pathAtOrBelow(result.out, rel)) return null;
  return result;
}

function remapGitStatus(status: GitStatus | null, oldRel: string, newRel: string): GitStatus | null {
  if (!status) return null;
  const remapChange = <T extends { path: string; oldPath: string }>(change: T): T => ({
    ...change,
    path: remapPath(change.path, oldRel, newRel),
    oldPath: change.oldPath ? remapPath(change.oldPath, oldRel, newRel) : change.oldPath,
  });
  return {
    ...status,
    staged: status.staged.map(remapChange),
    unstaged: status.unstaged.map(remapChange),
  };
}

function filterDeletedGitStatus(status: GitStatus | null, rel: string): GitStatus | null {
  if (!status) return null;
  const keep = (change: { path: string; oldPath: string }) =>
    !pathAtOrBelow(change.path, rel) && (!change.oldPath || !pathAtOrBelow(change.oldPath, rel));
  return { ...status, staged: status.staged.filter(keep), unstaged: status.unstaged.filter(keep) };
}

function remapPendingFileOp(op: PendingFileOp | null, oldRel: string, newRel: string): PendingFileOp | null {
  if (!op) return null;
  const targetRel = remapPath(op.targetRel, oldRel, newRel);
  return {
    ...op,
    targetRel,
    initial: op.type === "rename" && targetRel !== op.targetRel ? baseName(targetRel) : op.initial,
  };
}

function isEditableDirtyTab(tab: Tab): boolean {
  return tab.kind === "file" && !tab.binary && !tab.tooLarge && tab.content !== tab.savedContent;
}

export function isUnsavedResourceTab(tab: Tab): boolean {
	return isEditableDirtyTab(tab) || (tab.kind === "file" && !!tab.largeFileDirty);
}

function affectedTabs(state: State, rel: string): Tab[] {
  return state.tabs.filter((tab) => {
    const path = tabResourcePath(tab);
    return path !== null && pathAtOrBelow(path, rel);
  });
}

function knownRenameDestination(state: State, oldRel: string, newRel: string): boolean {
  if (state.allFiles.some((path) => path === newRel && !pathAtOrBelow(path, oldRel))) return true;
  if (
    state.tabs.some((tab) => {
      const path = tabResourcePath(tab);
      return path === newRel && !pathAtOrBelow(path, oldRel);
    })
  ) {
    return true;
  }
  return Object.values(state.childrenByPath).some((entries) =>
    entries.some((entry) => entry.path === newRel && !pathAtOrBelow(entry.path, oldRel)),
  );
}

function reconcileRenameState(state: State, oldRel: string, newRel: string): Partial<State> {
  const activeIndex = state.tabs.findIndex((tab) => tab.path === state.activePath);
  const tabs = state.tabs.map((tab) => remapTab(tab, oldRel, newRel));
  const childrenByPath = remapRecordKeys(state.childrenByPath, oldRel, newRel);
  for (const [path, entries] of Object.entries(childrenByPath)) {
    childrenByPath[path] = entries.map((entry) => remapEntry(entry, oldRel, newRel));
  }
  const loadingPath = remapRecordKeys(state.loadingPath, oldRel, newRel);
  for (const path of Object.keys(loadingPath)) {
    if (pathAtOrBelow(path, newRel)) loadingPath[path] = false;
  }
  return {
    tabs,
    activePath: activeIndex >= 0 ? tabs[activeIndex].path : state.activePath,
    selectedPath: state.selectedPath ? remapPath(state.selectedPath, oldRel, newRel) : null,
    childrenByPath,
    expanded: remapRecordKeys(state.expanded, oldRel, newRel),
    loadingPath,
    fileMenu: state.fileMenu
      ? (() => {
          const rel = remapPath(state.fileMenu.rel, oldRel, newRel);
          return rel === state.fileMenu.rel ? state.fileMenu : { ...state.fileMenu, rel, name: baseName(rel) };
        })()
      : null,
    pendingFileOp: remapPendingFileOp(state.pendingFileOp, oldRel, newRel),
    pendingDelete: state.pendingDelete
      ? (() => {
          const rel = remapPath(state.pendingDelete.rel, oldRel, newRel);
          return rel === state.pendingDelete.rel ? state.pendingDelete : { ...state.pendingDelete, rel, name: baseName(rel) };
        })()
      : null,
    pendingTabClose: state.pendingTabClose ? remapPath(state.pendingTabClose, oldRel, newRel) : null,
    pendingReveal: state.pendingReveal
      ? { ...state.pendingReveal, path: remapPath(state.pendingReveal.path, oldRel, newRel) }
      : null,
    searchResults: state.searchResults
      ? {
          ...state.searchResults,
          matches: state.searchResults.matches.map((match) => ({
            ...match,
            path: remapPath(match.path, oldRel, newRel),
          })),
        }
      : null,
    diagnostics: state.diagnostics
      ? {
          ...state.diagnostics,
          items: state.diagnostics.items.map((item) => ({
            ...item,
            path: remapPath(item.path, oldRel, newRel),
          })),
        }
      : null,
    allFiles: state.allFiles.map((path) => remapPath(path, oldRel, newRel)),
    gitStatus: remapGitStatus(state.gitStatus, oldRel, newRel),
    cleanDump: state.cleanDump ? { rel: remapPath(state.cleanDump.rel, oldRel, newRel) } : null,
    dataTools: state.dataTools ? { rel: remapPath(state.dataTools.rel, oldRel, newRel) } : null,
    toolResult: remapToolResult(state.toolResult, oldRel, newRel),
  };
}

interface DeleteReconciliation {
  patch: Partial<State>;
  preservedDirtyTabs: number;
}

function reconcileDeleteState(state: State, rel: string): DeleteReconciliation {
  let preservedDirtyTabs = 0;
  const tabs: Tab[] = [];
  for (const tab of state.tabs) {
    const resourcePath = tabResourcePath(tab);
    if (resourcePath === null || !pathAtOrBelow(resourcePath, rel)) {
      tabs.push(tab);
    } else if (isEditableDirtyTab(tab)) {
      // The user may have typed after confirming while the bridge deletion was
      // in flight. Keep that only in-memory copy as a recreatable draft.
      preservedDirtyTabs++;
      tabs.push({ ...tab, revision: "", staleOnDisk: true, staleRevision: undefined, saveRequestToken: undefined });
    }
  }
  const retainedPaths = new Set(tabs.map((tab) => tab.path));
  const activePath = state.activePath && retainedPaths.has(state.activePath)
    ? state.activePath
    : (tabs[tabs.length - 1]?.path ?? null);
  const childrenByPath = removeRecordPathKeys(state.childrenByPath, rel);
  for (const [path, entries] of Object.entries(childrenByPath)) {
    childrenByPath[path] = entries.filter((entry) => !pathAtOrBelow(entry.path, rel));
  }
  return {
    preservedDirtyTabs,
    patch: {
      tabs,
      activePath,
      selectedPath: state.selectedPath && pathAtOrBelow(state.selectedPath, rel) ? dirOf(rel) : state.selectedPath,
      childrenByPath,
      expanded: removeRecordPathKeys(state.expanded, rel),
      loadingPath: removeRecordPathKeys(state.loadingPath, rel),
      fileMenu: state.fileMenu && pathAtOrBelow(state.fileMenu.rel, rel) ? null : state.fileMenu,
      pendingFileOp:
        state.pendingFileOp && pathAtOrBelow(state.pendingFileOp.targetRel, rel) ? null : state.pendingFileOp,
      pendingDelete:
        state.pendingDelete && pathAtOrBelow(state.pendingDelete.rel, rel) ? null : state.pendingDelete,
      pendingTabClose:
        state.pendingTabClose && !retainedPaths.has(state.pendingTabClose) ? null : state.pendingTabClose,
      pendingTabCloseAttemptToken:
        state.pendingTabClose && !retainedPaths.has(state.pendingTabClose) ? null : state.pendingTabCloseAttemptToken,
      pendingTabCloseSaving:
        state.pendingTabClose && !retainedPaths.has(state.pendingTabClose) ? false : state.pendingTabCloseSaving,
      pendingReveal:
        state.pendingReveal && pathAtOrBelow(state.pendingReveal.path, rel) && !retainedPaths.has(state.pendingReveal.path)
          ? null
          : state.pendingReveal,
      searchResults: state.searchResults
        ? {
            ...state.searchResults,
            matches: state.searchResults.matches.filter((match) => !pathAtOrBelow(match.path, rel)),
          }
        : null,
      diagnostics: state.diagnostics
        ? {
            ...state.diagnostics,
            items: state.diagnostics.items.filter((item) => !pathAtOrBelow(item.path, rel)),
          }
        : null,
      allFiles: state.allFiles.filter((path) => !pathAtOrBelow(path, rel)),
      gitStatus: filterDeletedGitStatus(state.gitStatus, rel),
      cleanDump: state.cleanDump && pathAtOrBelow(state.cleanDump.rel, rel) ? null : state.cleanDump,
      dataTools: state.dataTools && pathAtOrBelow(state.dataTools.rel, rel) ? null : state.dataTools,
      toolResult: deleteToolResult(state.toolResult, rel),
    },
  };
}

function getSaveTarget(state: State, workspaceInstanceId: number, path: string, tabInstanceId: number): Tab | undefined {
  if (state.workspaceInstanceId !== workspaceInstanceId || state.workspaceTransitioning) return undefined;
  return state.tabs.find((tab) => tab.path === path && tab.instanceId === tabInstanceId);
}

function isCurrentGitWorkspace(state: State, workspaceInstanceId: number): boolean {
  return canReportWorkspaceDiagnostic(state, workspaceInstanceId);
}

function canReportWorkspaceDiagnostic(state: State, workspaceInstanceId: number): boolean {
  return state.workspaceInstanceId === workspaceInstanceId && state.isOpen && !state.workspaceTransitioning;
}

function reportCancellationFailure(
  getState: () => State,
  workspaceInstanceId: number,
  operation: "Agent" | "Assistant request",
  error: unknown,
): void {
  const state = getState();
  if (!canReportWorkspaceDiagnostic(state, workspaceInstanceId)) return;
  state.setStatus(`${operation} cancellation failed: ${errMessage(error)}`, "error");
}

function unavailableGitStatus(message: string): GitStatus {
  return { available: false, branch: "", head: "", aheadBehind: "", staged: [], unstaged: [], message } as GitStatus;
}

function gitMutationError(label: string, outcome: GitMutationOutcome<unknown>): string {
  const messages: string[] = [];
  if (outcome.mutationError) messages.push(`${label} failed: ${errMessage(outcome.mutationError)}`);
  if (outcome.refreshError) messages.push(`Git status refresh failed: ${errMessage(outcome.refreshError)}`);
  return messages.join(" ");
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

let statusTimer: ReturnType<typeof setTimeout> | undefined;

export const useStore = create<State>()((set, get) => ({
  workspaceInstanceId: 0,
  workspaceTransitioning: false,
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
  models: DEFAULT_OLLAMA_MODELS,
  streamReqId: null,
  streamMsgId: null,
  streamAttempt: 0,
  agentRunId: null,
  agentStarting: false,
  agentAttempt: 0,
  agentPlan: [],
  conversationResetting: false,
  conversationResetAttempt: 0,
  dbProfiles: [],
  dbCredentialStatuses: {},
  dbError: null,
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
  pendingTabCloseAttemptToken: null,
  pendingTabCloseSaving: false,
  toolResult: null,
  toolBusy: false,
  cleanDump: null,
  dataTools: null,
  aboutOpen: false,
  view: "explorer",
  sidebarVisible: true,
  panelVisible: false,
  panelMounted: false,
  panelTab: "terminal",
  jobs: [],
  artifacts: [],
  artifactsError: null,
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
  settingsError: null,
  llmAPIKeyStatus: "missing",
  llmAPIKeyStatusMessage: "",
  llmAPIKeyAvailable: false,
  appReady: false,

  init: async () => {
    try {
      const loaded = await loadAuthoritativeSettingsState();
      const s = loaded.settings;
      set({
        settings: s,
        settingsError: loaded.settingsError,
        llmAPIKeyStatus: loaded.llmAPIKeyStatus,
        llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
        llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
        recents: s.recentWorkspaces ?? [],
      });
      if (loaded.settingsError) get().setStatus(loaded.settingsError, "error");
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
    } finally {
      // Signal the boot splash that startup is done (settings loaded and, if
      // there was a last workspace, its tree opened) so it can fade out.
      set({ appReady: true });
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
    const before = get();
		if (before.tabs.some(isUnsavedResourceTab)) {
			get().setStatus("Save or explicitly discard all unsaved editor and large-file changes before switching workspaces.", "error");
			return;
		}
    const fallbackRoot = before.root;
    const fallbackWasOpen = before.isOpen;
    const transitionId = nextWorkspaceInstanceId();

    // Invalidate every operation that captured the previous workspace before
    // awaiting any bridge call. Keep the editor buffers until the latest Open
    // succeeds so a failed folder selection cannot discard unsaved work.
    set({
      workspaceInstanceId: transitionId,
      workspaceTransitioning: true,
      gitBusy: false,
      loadingPath: {},
      fileMenu: null,
      pendingFileOp: null,
      pendingDelete: null,
    });
    get().cancelChat();
    const isCurrent = () => {
      const st = get();
      return st.workspaceInstanceId === transitionId && st.workspaceTransitioning;
    };

    let opened: { info: Awaited<ReturnType<typeof Workspace.Open>> } | null;
    let backendOpenAttempted = false;
    let transitionToken: string | null = null;
    try {
      opened = await enqueueWorkspaceTransition(async () => {
        // Coalesce queued A -> B -> C requests: only the latest intent that has
        // not started its backend mutation is allowed to run.
        if (!isCurrent()) return null;
        // Begin is a backend shutdown/admission barrier. A timeout means old Agent work
        // may still reference the current root, so propagate it and abort the
        // workspace transition instead of opening the new root underneath it.
        transitionToken = await Agent.BeginWorkspaceTransition();
        if (!isCurrent()) return null;

        try {
          // Watcher state is process-global just like the workspace root. Stop
          // it inside the same serialized transition and do not mutate the root
          // unless shutdown was acknowledged.
          await enqueueWatchOperation(() => Watcher.Watch([]));
        } catch (watchError) {
          // A newer queued transition owns the still-active Agent barrier and
          // will retry watcher shutdown before its own root mutation.
          if (!isCurrent()) return null;
          try {
            await Agent.AbortWorkspaceTransition(transitionToken);
            transitionToken = null;
          } catch {
            // The outer recovery path retries settlement and reports a combined
            // failure if the barrier cannot be released.
          }
          throw watchError;
        }
        if (!isCurrent()) return null;

        backendOpenAttempted = true;
        const info = await Workspace.Open(path);
        if (!isCurrent()) return null;

        // RememberWorkspace is deliberately not awaited while this Agent/root
        // barrier is held. A slow settings call must not strand workspace
        // admission after the backend root has already changed.
        await Agent.EndWorkspaceTransition(transitionToken);
        transitionToken = null;
        if (!isCurrent()) {
          // End is the irreversible backend transcript commit. If a newer
          // workspace intent won while this bridge call was in flight, its UI
          // still contains the old chat but its subsequent Begin can only
          // snapshot the now-empty backend session. Clear that stale renderer
          // transcript before handing the transition queue to the newer owner.
          set({ chat: [], agentPlan: [] });
          return null;
        }
        return { info };
      });
    } catch (openError) {
      // A newer request owns recovery and final state.
      if (!isCurrent()) return;

      // Reset failed before any backend root mutation. Leave the current root
      // untouched: attempting even a redundant Open(fallbackRoot) while the old
      // Agent is still draining would violate the shutdown barrier we just
      // enforced.
      if (!backendOpenAttempted) {
        let transitionFailure: unknown = openError;
        if (transitionToken) {
          try {
            await Agent.AbortWorkspaceTransition(transitionToken);
            transitionToken = null;
          } catch (settlementError) {
            transitionFailure = new Error(
              `${errMessage(openError)}; workspace transition rollback failed: ${errMessage(settlementError)}`,
            );
          }
        }
        if (!isCurrent()) return;
        set({ workspaceTransitioning: false, loadingPath: {} });
        get().syncWatches();
        throw transitionFailure;
      }

      let restoreError: unknown = null;
      try {
        await enqueueWorkspaceTransition(async () => {
          if (!isCurrent()) return;
          if (fallbackWasOpen && fallbackRoot) {
            await Workspace.Open(fallbackRoot);
          } else {
            await Workspace.Close();
          }
        });
      } catch (error) {
        restoreError = error;
      }

      if (!isCurrent()) return;
      let endSettlementSucceeded = false;
      if (transitionToken) {
        try {
          if (restoreError) {
            await Agent.EndWorkspaceTransition(transitionToken);
            endSettlementSucceeded = true;
          } else {
            await Agent.AbortWorkspaceTransition(transitionToken);
          }
          transitionToken = null;
        } catch (error) {
          restoreError ??= error;
        }
      }
      if (!isCurrent()) {
        if (endSettlementSucceeded) set({ chat: [], agentPlan: [] });
        return;
      }

      if (restoreError) {
        // The old buffers remain in memory, but do not claim they are attached
        // to a backend root that could not be restored.
        set({
          workspaceTransitioning: false,
          root: "",
          wsName: "",
          isOpen: false,
          childrenByPath: {},
          expanded: {},
          loadingPath: {},
          selectedPath: null,
          chat: [],
          agentPlan: [],
        });
        get().setStatus(`Could not restore the previous workspace: ${errMessage(restoreError)}`, "error");
      } else {
        set({ workspaceTransitioning: false, loadingPath: {} });
        get().syncWatches();
      }
      throw openError;
    }

    if (!opened || !isCurrent()) return;
    const { info } = opened;
    const rememberSettingsToken = claimSettingsMutation();
    void enqueueSettingsMutation(() => Settings.RememberWorkspace(info.root)).then(
      (settings) => {
        if (rememberSettingsToken !== latestSettingsMutation) return;
        set({ settings, recents: settings.recentWorkspaces ?? [] });
      },
      async (error) => {
        let durableDiagnostic = "";
        try {
          durableDiagnostic = (await Settings.LoadError()).trim();
        } catch {
          // The mutation error still remains actionable when the separate
          // diagnostic binding is unavailable.
        }
        if (durableDiagnostic) {
          set({ settingsError: durableDiagnostic });
          get().setStatus(durableDiagnostic, "error");
        } else if (rememberSettingsToken === latestSettingsMutation) {
          get().setStatus(`Could not remember workspace: ${errMessage(error)}`, "error");
        }
      },
    );
    set({
      workspaceTransitioning: false,
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
      gitBusy: false,
      chat: [],
      agentPlan: [],
      pendingTabClose: null,
      pendingTabCloseAttemptToken: null,
      pendingTabCloseSaving: false,
      fileMenu: null,
      pendingFileOp: null,
      pendingDelete: null,
      activeDbId: null,
      dbTables: [],
      dbTablesLoading: false,
      dbSql: {},
      searchQuery: "",
      searchResults: null,
      searching: false,
      pendingReveal: null,
      diagnostics: null,
      loadingDiagnostics: false,
      jobs: [],
      artifacts: [],
      artifactsError: null,
      allFiles: [],
      allFilesError: null,
      loadingAllFiles: false,
      toolResult: null,
      toolBusy: false,
      cleanDump: null,
      dataTools: null,
      paletteOpen: false,
    });
    get().syncWatches();
    await get().loadChildren("");
    if (get().workspaceInstanceId !== transitionId || get().workspaceTransitioning) return;
    void get().loadGitStatus();
    void get().runDiagnostics();
    void get().loadArtifacts();
  },

  closeWorkspace: () => {
		if (get().tabs.some(isUnsavedResourceTab)) {
			get().setStatus("Save or explicitly discard all unsaved editor and large-file changes before closing the workspace.", "error");
			return;
		}
    // Claim transition ordering synchronously. A later Open/Close intent gets a
    // newer identity and cannot be closed by this older request after reset
    // finishes waiting.
    const transitionId = nextWorkspaceInstanceId();
    set({ workspaceInstanceId: transitionId, workspaceTransitioning: true, gitBusy: false, loadingPath: {} });
    // Cancel any in-flight LLM/agent run and close backend admission before a
    // queued root mutation can begin.
    get().cancelChat();
    void enqueueWorkspaceTransition(async () => {
      let transitionToken: string | null = null;
      let backendCloseSucceeded = false;
      const isCurrent = () => {
        const state = get();
        return state.workspaceInstanceId === transitionId && state.workspaceTransitioning;
      };
      try {
        if (!isCurrent()) return;
        transitionToken = await Agent.BeginWorkspaceTransition();
        // A stale successful Begin intentionally leaves its barrier in place.
        // The newer queued transition supersedes it before touching the root,
        // avoiding an admission gap between serialized transition intents.
        if (!isCurrent()) return;
        // Watcher shutdown and root closure are one ordered lifecycle change.
        // A rejected stop keeps the old root attached and enters recovery.
        await enqueueWatchOperation(() => Watcher.Watch([]));
        if (!isCurrent()) return;
        await Workspace.Close();
        backendCloseSucceeded = true;
        if (!isCurrent()) return;
        set({
          workspaceInstanceId: transitionId,
          workspaceTransitioning: true,
          root: "",
          wsName: "",
          isOpen: false,
          childrenByPath: {},
          expanded: {},
          tabs: [],
          activePath: null,
          selectedPath: null,
          gitStatus: null,
          gitBusy: false,
          loadingPath: {},
          // Clear per-workspace assistant / db-session / search / diagnostics / index
          // state so a newly opened workspace can't inherit the previous one's data.
          chat: [],
          agentPlan: [],
          pendingTabClose: null,
          pendingTabCloseAttemptToken: null,
          pendingTabCloseSaving: false,
          activeDbId: null,
          dbTables: [],
          dbTablesLoading: false,
          dbSql: {},
          fileMenu: null,
          pendingFileOp: null,
          pendingDelete: null,
          searchQuery: "",
          searchResults: null,
          searching: false,
          pendingReveal: null,
          diagnostics: null,
          loadingDiagnostics: false,
          jobs: [],
          artifacts: [],
          artifactsError: null,
          allFiles: [],
          allFilesError: null,
          loadingAllFiles: false,
          toolResult: null,
          toolBusy: false,
          cleanDump: null,
          dataTools: null,
          paletteOpen: false,
        });
        await Agent.EndWorkspaceTransition(transitionToken);
        transitionToken = null;
        if (isCurrent()) set({ workspaceTransitioning: false });
      } catch (error) {
        if (!isCurrent()) return;
        // Keep the current workspace attached when Begin or Close fails, and
        // release Agent admission for this latest, settled transition owner.
        let settlementFailed = false;
        if (transitionToken) {
          if (backendCloseSucceeded) {
            // The old root is already gone. Never restore its transcript by
            // aborting the transition; retry the forward-only settlement and
            // clear chat if bridge delivery remains ambiguous.
            try {
              await Agent.EndWorkspaceTransition(transitionToken);
              transitionToken = null;
            } catch {
              settlementFailed = true;
            }
          } else {
            try {
              await Agent.AbortWorkspaceTransition(transitionToken);
            } catch {
              settlementFailed = true;
            }
          }
        }
        if (!isCurrent()) {
          if (backendCloseSucceeded) set({ chat: [], agentPlan: [] });
          return;
        }
        if (settlementFailed) set({ chat: [], agentPlan: [] });
        set({ workspaceTransitioning: false });
        get().syncWatches();
        get().setStatus(errMessage(error), "error");
      }
    });
  },

  loadChildren: async (path: string) => {
    const workspaceInstanceId = get().workspaceInstanceId;
    if (!get().isOpen || get().workspaceTransitioning) return;
    const load = claimDirectoryLoad(workspaceInstanceId, path);
    const isCurrentLoad = (state: State) =>
      state.workspaceInstanceId === workspaceInstanceId &&
      !state.workspaceTransitioning &&
      latestDirectoryLoadAttempts.get(load.key) === load.attempt;
    set((st) =>
      isCurrentLoad(st)
        ? { loadingPath: { ...st.loadingPath, [path]: true } }
        : {},
    );
    try {
      const entries = await Workspace.ListDir(path);
      set((st) =>
        isCurrentLoad(st)
          ? { childrenByPath: { ...st.childrenByPath, [path]: entries } }
          : {},
      );
    } catch (e) {
      const st = get();
      if (isCurrentLoad(st)) get().setStatus(errMessage(e), "error");
    } finally {
      set((st) =>
        isCurrentLoad(st)
          ? { loadingPath: { ...st.loadingPath, [path]: false } }
          : {},
      );
      if (latestDirectoryLoadAttempts.get(load.key) === load.attempt) latestDirectoryLoadAttempts.delete(load.key);
    }
  },

  toggleDir: async (path: string) => {
    if (get().workspaceTransitioning) return;
    const open = !get().expanded[path];
    set((st) => ({ expanded: { ...st.expanded, [path]: open }, selectedPath: path }));
    if (open && !get().childrenByPath[path]) {
      await get().loadChildren(path);
    }
  },

  refreshTree: async () => {
    if (get().workspaceTransitioning) return;
    const loaded = Object.keys(get().childrenByPath);
    // Reload the expanded directories concurrently rather than serially.
    await Promise.all(loaded.map((p) => get().loadChildren(p)));
    void get().loadGitStatus();
  },

  openFile: async (path: string, name: string) => {
    const requestState = get();
    if (!requestState.isOpen || requestState.workspaceTransitioning) return;
    const workspaceInstanceId = requestState.workspaceInstanceId;
    const fileGeneration = workspaceFileStateGeneration;
    set({ selectedPath: path });
    const existing = requestState.tabs.find((t) => t.path === path);
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
      set((st) => {
        if (
          st.workspaceInstanceId !== workspaceInstanceId ||
          st.workspaceTransitioning ||
          pathChangedSince(fileGeneration, path)
        ) {
          return {};
        }
        return st.tabs.some((t) => t.path === path) ? { activePath: path } : { tabs: [...st.tabs, tab], activePath: path };
      });

    // Tabular files open in the analytics grid (TableView fetches by rel).
    if (isTabular(path)) {
      appendTab({
        instanceId: nextTabInstanceId(),
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
        instanceId: nextTabInstanceId(),
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
      if (get().workspaceInstanceId !== workspaceInstanceId || get().workspaceTransitioning) return;
      get().syncWatches();
    } catch (e) {
      const st = get();
      if (st.workspaceInstanceId === workspaceInstanceId && !st.workspaceTransitioning) get().setStatus(errMessage(e), "error");
    }
  },

  syncWatches: () => {
    const st = get();
    if (st.workspaceTransitioning || !st.isOpen) return;
    const workspaceInstanceId = st.workspaceInstanceId;
    const attempt = ++watchSyncAttemptCounter;
    latestWatchSyncAttempt = attempt;
    const files = [
      ...new Set(
        st.tabs
          .filter((tab) => tab.kind === "file" || tab.kind === "table")
          .map(tabResourcePath)
          .filter((path): path is string => path !== null),
      ),
    ];
    void enqueueWatchOperation(async () => {
      if (!canReportWorkspaceDiagnostic(get(), workspaceInstanceId)) return;
      await Watcher.Watch(files);
    }).catch((error) => {
      const current = get();
      if (!canReportWorkspaceDiagnostic(current, workspaceInstanceId) || attempt !== latestWatchSyncAttempt) return;
      current.setStatus(`File watcher update failed: ${errMessage(error)}`, "error");
    });
  },

  reloadIfChanged: async (path: string) => {
    const requestState = get();
    if (requestState.workspaceTransitioning) return;
    const requestTab = requestState.tabs.find(
      (tab) => (tab.kind === "file" && tab.path === path) || (tab.kind === "table" && tab.rel === path),
    );
    if (!requestTab) return;
    const workspaceInstanceId = requestState.workspaceInstanceId;
    const tabInstanceId = requestTab.instanceId;
    recordFileStateMutation(path);
    set((state) => {
      if (state.workspaceInstanceId !== workspaceInstanceId || state.workspaceTransitioning) return {};
      const invalidated = { allFiles: [], allFilesError: null, searchResults: null, diagnostics: null };
      if (requestTab.kind !== "table") return invalidated;
      return {
        ...invalidated,
        tabs: state.tabs.map((tab) =>
          tab.kind === "table" && tab.instanceId === tabInstanceId
            ? { ...tab, sourceVersion: (tab.sourceVersion ?? 0) + 1 }
            : tab,
        ),
      };
    });
    void get().loadChildren(dirOf(path));
    if (requestTab.kind === "table") return;
    try {
      const fc = await Workspace.ReadFile(path);
      // Re-read the tab from CURRENT state inside the updater: a save that landed
      // during the await above must win, otherwise it could be resurrected as
      // staleOnDisk against a pre-await snapshot.
      set((st) => {
        if (st.workspaceInstanceId !== workspaceInstanceId || st.workspaceTransitioning) return {};
        const tab = st.tabs.find((t) => t.path === path && t.kind === "file" && t.instanceId === tabInstanceId);
        if (!tab) return {}; // tab closed during the await
        if (fc.revision === tab.revision) {
          if (!tab.staleOnDisk) return {};
          return {
            tabs: st.tabs.map((t) =>
              t.instanceId === tabInstanceId ? { ...t, staleOnDisk: false, staleRevision: undefined } : t,
            ),
          };
        }
        if (isUnsavedResourceTab(tab)) {
          // Don't clobber unsaved edits — flag a conflict the user resolves.
          return {
            tabs: st.tabs.map((t) =>
              t.instanceId === tabInstanceId ? { ...t, staleOnDisk: true, staleRevision: fc.revision } : t,
            ),
          };
        }
        return {
          tabs: st.tabs.map((t) =>
            t.instanceId === tabInstanceId
              ? {
                  ...t,
                  content: fc.content,
                  savedContent: fc.content,
                  revision: fc.revision,
                  binary: fc.binary,
                  tooLarge: fc.tooLarge,
                  encoding: fc.encoding,
                  staleOnDisk: false,
                  staleRevision: undefined,
                  sourceVersion: (t.sourceVersion ?? 0) + 1,
                }
              : t,
          ),
        };
      });
    } catch {
      /* file removed or unreadable — leave the tab as-is */
    }
  },

  reloadTab: async (path: string) => {
    const requestState = get();
    if (requestState.workspaceTransitioning) return;
    const requestTab = requestState.tabs.find((t) => t.path === path && t.kind === "file");
    if (!requestTab) return;
    const workspaceInstanceId = requestState.workspaceInstanceId;
    const tabInstanceId = requestTab.instanceId;
    const requestedContent = requestTab.content;
    const requestedSavedContent = requestTab.savedContent;
    try {
      const fc = await Workspace.ReadFile(path);
      let applied = false;
      let changedDuringReload = false;
      set((st) => {
        if (st.workspaceInstanceId !== workspaceInstanceId || st.workspaceTransitioning) return {};
        return {
          tabs: st.tabs.map((t) => {
            if (t.path !== path || t.instanceId !== tabInstanceId) return t;
            if (t.content !== requestedContent || t.savedContent !== requestedSavedContent) {
              changedDuringReload = true;
              return t;
            }
            applied = true;
            return {
              ...t,
              content: fc.content,
              savedContent: fc.content,
              revision: fc.revision,
              binary: fc.binary,
              tooLarge: fc.tooLarge,
              encoding: fc.encoding,
              staleOnDisk: false,
              staleRevision: undefined,
              sourceVersion: (t.sourceVersion ?? 0) + 1,
            };
          }),
        };
      });
      if (!applied && changedDuringReload) {
        get().setStatus("Reload was cancelled because the editor changed while the disk version was loading.", "error");
      }
    } catch (e) {
      const st = get();
      if (st.workspaceInstanceId === workspaceInstanceId && !st.workspaceTransitioning) get().setStatus(errMessage(e), "error");
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
		const dirty = !!tab && isUnsavedResourceTab(tab);
    if (dirty) {
      set({
        pendingTabClose: path,
        pendingTabCloseAttemptToken: nextCloseAttemptToken(),
        pendingTabCloseSaving: false,
      });
    } else {
      get().closeTab(path);
    }
  },

	setLargeFileState: (path, state) => {
		set((current) => ({
			tabs: current.tabs.map((tab) =>
				tab.path === path && tab.kind === "file" && tab.tooLarge
					? {
							...tab,
							largeFileSessionId: state.sessionId ?? tab.largeFileSessionId,
							largeFileDirty: state.dirty ?? tab.largeFileDirty,
							largeFileInPlaceEligible: state.inPlaceEligible ?? tab.largeFileInPlaceEligible,
						}
					: tab,
			),
		}));
	},

  confirmCloseTab: async (save: boolean) => {
    const { pendingTabClose: path, pendingTabCloseAttemptToken: attemptToken, pendingTabCloseSaving } = get();
    if (!path || attemptToken == null) return;
    if (pendingTabCloseSaving) return;
		const requestedTab = get().tabs.find((tab) => tab.path === path);
		if (requestedTab?.largeFileDirty) {
			if (save) {
				get().setStatus("Save the staged large-file edits from the editor, then close the tab.", "error");
				return;
			}
			if (!requestedTab.largeFileSessionId) {
				get().setStatus("The large-file session is unavailable; the staged edits were not discarded.", "error");
				return;
			}
			set({ pendingTabCloseSaving: true });
			try {
				await BigFile.DiscardEdits(requestedTab.largeFileSessionId);
			} catch (error) {
				const current = get();
				if (current.pendingTabClose === path && current.pendingTabCloseAttemptToken === attemptToken) {
					set({ pendingTabCloseSaving: false });
					get().setStatus(`Could not discard staged edits: ${errMessage(error)}`, "error");
				}
				return;
			}
			const current = get();
			if (current.pendingTabClose !== path || current.pendingTabCloseAttemptToken !== attemptToken) return;
			set({ pendingTabClose: null, pendingTabCloseAttemptToken: null, pendingTabCloseSaving: false });
			get().closeTab(path);
			return;
		}

    if (!save) {
      set({ pendingTabClose: null, pendingTabCloseAttemptToken: null, pendingTabCloseSaving: false });
      get().closeTab(path);
      return;
    }

    set({ pendingTabCloseSaving: true });
    const result = await get().saveTab(path);

    // Cancelling the dialog while the bridge call is in flight must not turn
    // into a close when that older request eventually finishes.
    const pendingClose = get();
    if (pendingClose.pendingTabClose !== path || pendingClose.pendingTabCloseAttemptToken !== attemptToken) return;

    const tab = get().tabs.find((t) => t.path === path);
    const fullySaved = !!tab && tab.content === tab.savedContent;
    if (result.ok && fullySaved) {
      set({ pendingTabClose: null, pendingTabCloseAttemptToken: null, pendingTabCloseSaving: false });
      get().closeTab(path);
      return;
    }

    // A conflict/failure or a newer edit keeps the dialog and the only
    // in-memory copy open so the user can retry or cancel deliberately.
    set({ pendingTabCloseSaving: false });
  },

  cancelCloseTab: () => set({ pendingTabClose: null, pendingTabCloseAttemptToken: null, pendingTabCloseSaving: false }),

  runCsvSchema: async () => {
    const { csv, path } = activeResourceCapabilities(get().tabs, get().activePath);
    if (!csv || !path) {
      get().setStatus("Open a CSV or TSV file first to infer its schema.", "error");
      return;
    }
    const name = path.split("/").pop() ?? path;
    const operation = toolOperation.tryAcquire("CSV schema");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true, toolResult: null });
    try {
      const schema = await Workspace.InferTableSchema(path, "");
      set({ toolResult: { kind: "schema", title: `Schema · ${name}`, rel: path, schema } });
    } catch (e) {
      set({ toolResult: { kind: "error", title: `Schema · ${name}`, message: errMessage(e) } });
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  runDumpAnalyze: async () => {
    const { dump, path } = activeResourceCapabilities(get().tabs, get().activePath);
    if (!dump || !path) {
      get().setStatus("Open a .sql or .dump file first to analyze it.", "error");
      return;
    }
    const name = path.split("/").pop() ?? path;
    const operation = toolOperation.tryAcquire("SQL dump analysis");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true, toolResult: null });
    try {
      const dump = await Workspace.AnalyzeSQLDump(path);
      set({ toolResult: { kind: "dump", title: `SQL dump · ${name}`, rel: path, dump } });
    } catch (e) {
      set({ toolResult: { kind: "error", title: `SQL dump · ${name}`, message: errMessage(e) } });
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  runCsvToSql: async () => {
    const { csv, path } = activeResourceCapabilities(get().tabs, get().activePath);
    if (!csv || !path) {
      get().setStatus("Open a CSV or TSV file first to convert it to SQL.", "error");
      return;
    }
    const name = path.split("/").pop() ?? path;
    const table = name.replace(/\.[^.]+$/, "");
    const dir = dirOf(path);
    const out = dir ? `${dir}/${table}.sql` : `${table}.sql`;
    const operation = toolOperation.tryAcquire("CSV to SQL preview");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true, toolResult: null });
    try {
      const sql = await Workspace.PreviewCsvToSql(path, table, true);
      set({ toolResult: { kind: "sql", title: `CSV → SQL · ${name}`, rel: path, table, out, sql } });
    } catch (e) {
      set({ toolResult: { kind: "error", title: `CSV → SQL · ${name}`, message: errMessage(e) } });
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  saveCsvToSql: async (rel, outRel, table) => {
    const operation = toolOperation.tryAcquire("CSV to SQL export");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const sum = await Workspace.ConvertCsvToSql(rel, outRel, table || "data", true);
      set({ toolResult: null });
      get().setStatus(`Wrote ${outRel} (${sum.rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  extractDumpTable: async (rel, table) => {
    const dir = dirOf(rel);
    const out = dir ? `${dir}/${table}.sql` : `${table}.sql`;
    const operation = toolOperation.tryAcquire("SQL table extraction");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const res = await Workspace.ExtractDumpTable(rel, table, out);
      set({ toolResult: null });
      get().setStatus(`Extracted ${table} → ${out} (${res.bytes.toLocaleString()} bytes)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  splitDump: async (rel) => {
    const name = (rel.split("/").pop() ?? rel).replace(/\.[^.]+$/, "");
    const dir = dirOf(rel);
    const outDir = dir ? `${dir}/${name}-tables` : `${name}-tables`;
    const operation = toolOperation.tryAcquire("SQL dump split");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const res = await Workspace.SplitDump(rel, outDir);
      set({ toolResult: null });
      get().setStatus(`Split into ${res.tables} files under ${outDir}`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  dumpTableToCsv: async (rel, table) => {
    const dir = dirOf(rel);
    const out = dir ? `${dir}/${table}.csv` : `${table}.csv`;
    const operation = toolOperation.tryAcquire("SQL table to CSV export");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const rows = await Workspace.DumpTableToCsv(rel, table, out);
      set({ toolResult: null });
      get().setStatus(`Extracted ${table} → ${out} (${rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
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
    const operation = toolOperation.tryAcquire("CSV column export");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const rows = await Workspace.AddCsvColumn(rel, out, name.trim(), value);
      set({ toolResult: null });
      get().setStatus(`Wrote ${out} (added "${name.trim()}", ${rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
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
    const operation = toolOperation.tryAcquire("CSV column projection");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const rows = await Workspace.ProjectCsv(rel, out, columns);
      set({ toolResult: null });
      get().setStatus(`Wrote ${out} (${columns.length} cols, ${rows.toLocaleString()} rows)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
    }
  },

  openCleanDump: () => {
    const { dump, path } = activeResourceCapabilities(get().tabs, get().activePath);
    if (!dump || !path) {
      get().setStatus("Open a .sql or .dump file first to clean it.", "error");
      return;
    }
    set({ cleanDump: { rel: path } });
  },

  cancelCleanDump: () => set({ cleanDump: null }),

  openDataTools: () => {
    const { csv, dump, path } = activeResourceCapabilities(get().tabs, get().activePath);
    if ((!csv && !dump) || !path) {
      get().setStatus("Open a .csv, .tsv, .sql, or .dump file to use data tools.", "error");
      return;
    }
    set({ dataTools: { rel: path } });
  },

  closeDataTools: () => set({ dataTools: null }),

  openAbout: () => set({ aboutOpen: true }),
  closeAbout: () => set({ aboutOpen: false }),

  applyCleanDump: async (rel, outRel, t) => {
    const operation = toolOperation.tryAcquire("SQL dump cleanup");
    if (!operation) {
      get().setStatus(`Wait for ${toolOperation.activeLabel() ?? "the current data tool"} to finish.`, "error");
      return;
    }
    set({ toolBusy: true });
    try {
      const sum = await Workspace.TransformDump(rel, outRel, t);
      set({ cleanDump: null });
      get().setStatus(`Wrote ${outRel} (${sum.replacements.toLocaleString()} replacements)`, "success");
      void get().refreshTree();
    } catch (e) {
      get().setStatus(errMessage(e), "error");
    } finally {
      if (toolOperation.release(operation)) set({ toolBusy: false });
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
  convertEncoding: (path: string, encoding: string) => {
    const enqueuedState = get();
    const enqueuedTab = enqueuedState.tabs.find((tab) => tab.path === path);
    if (!enqueuedTab || enqueuedTab.kind !== "file" || enqueuedTab.binary || enqueuedTab.tooLarge) {
      return Promise.resolve<TabSaveResult>({ ok: false, outcome: "unavailable", message: "File cannot be saved" });
    }
    const workspaceInstanceId = enqueuedState.workspaceInstanceId;
    const tabInstanceId = enqueuedTab.instanceId;
    if (hasActiveFileMutation(workspaceInstanceId, path)) {
      const message = "Wait for the file rename or delete to finish before saving.";
      get().setStatus(message, "error");
      return Promise.resolve<TabSaveResult>({
        ok: false,
        outcome: "unavailable",
        message,
      });
    }

    return enqueueTabSave(workspaceInstanceId, path, async () => {
      const tab = getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId);
      if (!tab) {
        return { ok: false, outcome: "superseded", message: "Save target was replaced" };
      }
      if (tab.kind !== "file" || tab.binary || tab.tooLarge) {
        return { ok: false, outcome: "unavailable", message: "File cannot be saved" };
      }
      if ((tab.encoding ?? "utf-8") === encoding) {
        return { ok: true, outcome: "unchanged", submittedContent: tab.savedContent };
      }

      const submittedContent = tab.content;
      const submittedRevision = tab.revision;
      const saveRequestToken = nextSaveRequestToken();
      set((st) => {
        if (st.workspaceInstanceId !== workspaceInstanceId) return {};
        return {
          tabs: st.tabs.map((t) =>
            t.path === path && t.instanceId === tabInstanceId ? { ...t, saveRequestToken } : t,
          ),
        };
      });
      try {
        const res = await Workspace.WriteFile(path, submittedContent, submittedRevision, encoding);
        if (!getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) {
          return { ok: false, outcome: "superseded", submittedContent, message: "Save target was replaced" };
        }
        let applied = false;
        let newerEditsRemain = false;
        set((st) => {
          if (st.workspaceInstanceId !== workspaceInstanceId) return {};
          return {
            tabs: st.tabs.map((t) =>
              t.path === path &&
              t.instanceId === tabInstanceId &&
              t.revision === submittedRevision &&
              t.saveRequestToken === saveRequestToken
                ? (() => {
                    applied = true;
                    newerEditsRemain = t.content !== submittedContent;
                    return {
                      ...t,
                      encoding,
                      savedContent: submittedContent,
                      revision: res.revision,
                      staleOnDisk: false,
                      staleRevision: undefined,
                    };
                  })()
                : t,
            ),
          };
        });
        if (!applied) {
          const message = `${tab.name} was saved, but the editor changed before the result was applied.`;
          if (getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) get().setStatus(message, "error");
          return { ok: false, outcome: "superseded", submittedContent, message };
        }
        get().setStatus(
          newerEditsRemain ? `Saved ${tab.name} as ${encoding}; newer edits remain unsaved` : `Saved ${tab.name} as ${encoding}`,
          "success",
        );
        return { ok: true, outcome: "saved", submittedContent };
      } catch (e) {
        const message = errMessage(e);
        if (!getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) {
          return { ok: false, outcome: "superseded", submittedContent, message };
        }
        if (message.toLowerCase().includes(STALE_MARKER)) {
          void get().reloadIfChanged(path);
        }
        get().setStatus(message, "error");
        return { ok: false, outcome: "failed", submittedContent, message };
      }
    });
  },

  saveTab: (path: string) => {
    const enqueuedState = get();
    const enqueuedTab = enqueuedState.tabs.find((tab) => tab.path === path);
    if (!enqueuedTab || enqueuedTab.kind !== "file" || enqueuedTab.binary || enqueuedTab.tooLarge) {
      return Promise.resolve<TabSaveResult>({ ok: false, outcome: "unavailable", message: "File cannot be saved" });
    }
    const workspaceInstanceId = enqueuedState.workspaceInstanceId;
    const tabInstanceId = enqueuedTab.instanceId;
    if (hasActiveFileMutation(workspaceInstanceId, path)) {
      const message = "Wait for the file rename or delete to finish before saving.";
      get().setStatus(message, "error");
      return Promise.resolve<TabSaveResult>({
        ok: false,
        outcome: "unavailable",
        message,
      });
    }

    return enqueueTabSave(workspaceInstanceId, path, async () => {
      const tab = getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId);
      if (!tab) {
        return { ok: false, outcome: "superseded", message: "Save target was replaced" };
      }
      if (tab.kind !== "file" || tab.binary || tab.tooLarge) {
        return { ok: false, outcome: "unavailable", message: "File cannot be saved" };
      }
      if (tab.content === tab.savedContent) {
        return { ok: true, outcome: "unchanged", submittedContent: tab.savedContent };
      }

      const submittedContent = tab.content;
      const submittedRevision = tab.revision;
      const saveRequestToken = nextSaveRequestToken();
      set((st) => {
        if (st.workspaceInstanceId !== workspaceInstanceId) return {};
        return {
          tabs: st.tabs.map((t) =>
            t.path === path && t.instanceId === tabInstanceId ? { ...t, saveRequestToken } : t,
          ),
        };
      });
      try {
        // Round-trip the file's detected encoding so saving a UTF-16/Latin-1 file
        // doesn't silently rewrite it as UTF-8 (empty = UTF-8 for new files).
        const res = await Workspace.WriteFile(path, submittedContent, submittedRevision, tab.encoding ?? "");
        if (!getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) {
          return { ok: false, outcome: "superseded", submittedContent, message: "Save target was replaced" };
        }
        let applied = false;
        let newerEditsRemain = false;
        set((st) => {
          if (st.workspaceInstanceId !== workspaceInstanceId) return {};
          return {
            tabs: st.tabs.map((t) =>
              t.path === path &&
              t.instanceId === tabInstanceId &&
              t.revision === submittedRevision &&
              t.saveRequestToken === saveRequestToken
                ? (() => {
                    applied = true;
                    newerEditsRemain = t.content !== submittedContent;
                    return {
                      ...t,
                      savedContent: submittedContent,
                      revision: res.revision,
                      staleOnDisk: false,
                      staleRevision: undefined,
                    };
                  })()
                : t,
            ),
          };
        });
        if (!applied) {
          const message = `${tab.name} was saved, but the editor changed before the result was applied.`;
          if (getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) get().setStatus(message, "error");
          return { ok: false, outcome: "superseded", submittedContent, message };
        }
        get().setStatus(newerEditsRemain ? `Saved ${tab.name}; newer edits remain unsaved` : `Saved ${tab.name}`, "success");
        void get().loadGitStatus();
        return { ok: true, outcome: "saved", submittedContent };
      } catch (e) {
        const msg = errMessage(e);
        if (!getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) {
          return { ok: false, outcome: "superseded", submittedContent, message: msg };
        }
        if (msg.toLowerCase().includes(STALE_MARKER)) {
          void get().reloadIfChanged(path);
          get().setStatus(`${tab.name} changed on disk — save refused. Choose how to resolve the conflict.`, "error");
        } else {
          get().setStatus(msg, "error");
        }
        return { ok: false, outcome: "failed", submittedContent, message: msg };
      }
    });
  },

  overwriteStaleTab: (path: string) => {
    const enqueuedState = get();
    const enqueuedTab = enqueuedState.tabs.find((tab) => tab.path === path);
    if (
      !enqueuedTab ||
      enqueuedTab.kind !== "file" ||
      enqueuedTab.binary ||
      enqueuedTab.tooLarge ||
      !enqueuedTab.staleOnDisk ||
      !enqueuedTab.staleRevision
    ) {
      const message = "The exact disk revision is unavailable; wait for refresh and retry.";
      get().setStatus(message, "error");
      return Promise.resolve<TabSaveResult>({ ok: false, outcome: "unavailable", message });
    }
    const workspaceInstanceId = enqueuedState.workspaceInstanceId;
    const tabInstanceId = enqueuedTab.instanceId;
    if (hasActiveFileMutation(workspaceInstanceId, path)) {
      const message = "Wait for the file rename or delete to finish before resolving the conflict.";
      get().setStatus(message, "error");
      return Promise.resolve<TabSaveResult>({ ok: false, outcome: "unavailable", message });
    }

    return enqueueTabSave(workspaceInstanceId, path, async () => {
      const tab = getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId);
      if (!tab) {
        return { ok: false, outcome: "superseded", message: "Conflict target was replaced" };
      }
      if (
        tab.kind !== "file" ||
        tab.binary ||
        tab.tooLarge ||
        !tab.staleOnDisk ||
        !tab.staleRevision
      ) {
        return { ok: false, outcome: "unavailable", message: "The conflict is no longer current" };
      }

      const submittedContent = tab.content;
      const expectedDiskRevision = tab.staleRevision;
      const saveRequestToken = nextSaveRequestToken();
      set((state) => {
        if (state.workspaceInstanceId !== workspaceInstanceId) return {};
        return {
          tabs: state.tabs.map((candidate) =>
            candidate.path === path && candidate.instanceId === tabInstanceId
              ? { ...candidate, saveRequestToken }
              : candidate,
          ),
        };
      });

      try {
        // This is an explicit overwrite, but it is not unconditional: the
        // backend must still observe the exact external revision shown by the
        // conflict UI. A second external edit makes this call fail stale.
        const result = await Workspace.WriteFile(path, submittedContent, expectedDiskRevision, tab.encoding ?? "");
        if (!getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) {
          return { ok: false, outcome: "superseded", submittedContent, message: "Conflict target was replaced" };
        }
        let applied = false;
        let newerEditsRemain = false;
        set((state) => {
          if (state.workspaceInstanceId !== workspaceInstanceId) return {};
          return {
            tabs: state.tabs.map((candidate) => {
              if (
                candidate.path !== path ||
                candidate.instanceId !== tabInstanceId ||
                candidate.staleRevision !== expectedDiskRevision ||
                candidate.saveRequestToken !== saveRequestToken
              ) {
                return candidate;
              }
              applied = true;
              newerEditsRemain = candidate.content !== submittedContent;
              return {
                ...candidate,
                savedContent: submittedContent,
                revision: result.revision,
                staleOnDisk: false,
                staleRevision: undefined,
              };
            }),
          };
        });
        if (!applied) {
          const message = `${tab.name} was overwritten, but a newer conflict state replaced the result.`;
          get().setStatus(message, "error");
          return { ok: false, outcome: "superseded", submittedContent, message };
        }
        get().setStatus(
          newerEditsRemain
            ? `Overwrote the observed disk version of ${tab.name}; newer editor changes remain unsaved`
            : `Overwrote the observed disk version of ${tab.name}`,
          "success",
        );
        void get().loadGitStatus();
        return { ok: true, outcome: "saved", submittedContent };
      } catch (error) {
        const message = errMessage(error);
        if (!getSaveTarget(get(), workspaceInstanceId, path, tabInstanceId)) {
          return { ok: false, outcome: "superseded", submittedContent, message };
        }
        if (message.toLowerCase().includes(STALE_MARKER)) {
          void get().reloadIfChanged(path);
          get().setStatus(`${tab.name} changed again; overwrite was refused. Review the newer disk version.`, "error");
        } else {
          get().setStatus(message, "error");
        }
        return { ok: false, outcome: "failed", submittedContent, message };
      }
    });
  },

  loadGitStatus: async () => {
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) {
      if (!requested.isOpen) set({ gitStatus: null, gitBusy: false });
      return;
    }
    const workspaceInstanceId = requested.workspaceInstanceId;
    const attempt = claimGitOperation();
    set((st) => (isCurrentGitWorkspace(st, workspaceInstanceId) ? { gitBusy: true } : {}));
    try {
      await enqueueGitOperation(async () => {
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        try {
          const status = await Git.Status();
          set((st) => (isCurrentGitWorkspace(st, workspaceInstanceId) ? { gitStatus: status } : {}));
        } catch (error) {
          if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
          const message = errMessage(error);
          // Surface the failure in the panel instead of leaving it stuck on
          // "Loading…" forever when the very first status call throws.
          set({ gitStatus: unavailableGitStatus(message) });
          get().setStatus(message, "error");
        }
      });
    } finally {
      set((st) =>
        isCurrentGitWorkspace(st, workspaceInstanceId) && attempt === latestGitOperationAttempt ? { gitBusy: false } : {},
      );
    }
  },

  stageFile: async (rel: string) => {
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const attempt = claimGitOperation();
    set((st) => (isCurrentGitWorkspace(st, workspaceInstanceId) ? { gitBusy: true } : {}));
    try {
      await enqueueGitOperation(async () => {
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const outcome = await mutateAndRefreshGit(() => Git.Stage(rel), (status) => status);
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const message = gitMutationError("Stage", outcome);
        set({ gitStatus: outcome.status ?? unavailableGitStatus(message || "Git status is unavailable.") });
        if (message) get().setStatus(message, "error");
      });
    } finally {
      set((st) =>
        isCurrentGitWorkspace(st, workspaceInstanceId) && attempt === latestGitOperationAttempt ? { gitBusy: false } : {},
      );
    }
  },

  unstageFile: async (rel: string) => {
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const attempt = claimGitOperation();
    set((st) => (isCurrentGitWorkspace(st, workspaceInstanceId) ? { gitBusy: true } : {}));
    try {
      await enqueueGitOperation(async () => {
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const outcome = await mutateAndRefreshGit(() => Git.Unstage(rel), (status) => status);
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const message = gitMutationError("Unstage", outcome);
        set({ gitStatus: outcome.status ?? unavailableGitStatus(message || "Git status is unavailable.") });
        if (message) get().setStatus(message, "error");
      });
    } finally {
      set((st) =>
        isCurrentGitWorkspace(st, workspaceInstanceId) && attempt === latestGitOperationAttempt ? { gitBusy: false } : {},
      );
    }
  },

  stageAll: async () => {
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const attempt = claimGitOperation();
    set((st) => (isCurrentGitWorkspace(st, workspaceInstanceId) ? { gitBusy: true } : {}));
    try {
      await enqueueGitOperation(async () => {
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const outcome = await mutateAndRefreshGit(() => Git.StageAll(), (status) => status);
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const message = gitMutationError("Stage all", outcome);
        set({ gitStatus: outcome.status ?? unavailableGitStatus(message || "Git status is unavailable.") });
        if (message) get().setStatus(message, "error");
      });
    } finally {
      set((st) =>
        isCurrentGitWorkspace(st, workspaceInstanceId) && attempt === latestGitOperationAttempt ? { gitBusy: false } : {},
      );
    }
  },

  commit: async (subject: string, body: string) => {
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) return false;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const attempt = claimGitOperation();
    let committed = false;
    set((st) => (isCurrentGitWorkspace(st, workspaceInstanceId) ? { gitBusy: true } : {}));
    try {
      await enqueueGitOperation(async () => {
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const outcome = await mutateAndRefreshGit(
          () => Git.Commit(subject, body),
          (result) => result.status ?? null,
        );
        if (!isCurrentGitWorkspace(get(), workspaceInstanceId)) return;
        const message = gitMutationError("Commit", outcome);
        set({ gitStatus: outcome.status ?? unavailableGitStatus(message || "Git status is unavailable.") });
        if (message) {
          get().setStatus(message, "error");
          committed = !!outcome.result && (outcome.result.committed || !!outcome.result.hash);
          return;
        }
        const result = outcome.result;
        if (!result) return;
        committed = result.committed || !!result.hash;
        if (committed) {
          const label = result.shortHash ? `Committed ${result.shortHash}` : result.message || "Commit succeeded.";
          get().setStatus(label, "success");
        } else {
          get().setStatus(result.message || "Nothing was committed.", "info");
        }
      });
      return committed;
    } finally {
      set((st) =>
        isCurrentGitWorkspace(st, workspaceInstanceId) && attempt === latestGitOperationAttempt ? { gitBusy: false } : {},
      );
    }
  },

  openDiff: async (rel: string, oldRel = "", staged = false) => {
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const fileGeneration = workspaceFileStateGeneration;
    // Key by staged state so the staged and unstaged diffs of one file open as
    // two distinct tabs instead of overwriting each other.
    const key = `diff:${staged ? "s" : "u"}:${rel}`;
    const label = `${baseName(rel)} ${staged ? "(staged)" : "(working tree)"}`;
    try {
      const d = await Git.Diff(rel, oldRel, staged);
      const current = get();
      if (
        !canReportWorkspaceDiagnostic(current, workspaceInstanceId) ||
        pathChangedSince(fileGeneration, rel) ||
        (!!oldRel && pathChangedSince(fileGeneration, oldRel))
      ) {
        return;
      }
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
        instanceId: nextTabInstanceId(),
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
      if (canReportWorkspaceDiagnostic(get(), workspaceInstanceId) && !pathChangedSince(fileGeneration, rel)) {
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  toggleAssistant: () => set((st) => ({ assistantVisible: !st.assistantVisible })),
  toggleAgentMode: () => set((st) => ({ agentMode: !st.agentMode })),

  sendAgent: async (text: string) => {
    const content = text.trim();
    const st = get();
    if (!content || !st.isOpen || st.chatStreaming || st.workspaceTransitioning || st.conversationResetting) return;
    const workspaceInstanceId = st.workspaceInstanceId;
    agentEventQueues.clear();
    agentEventQueueFailures.clear();
    const attempt = st.agentAttempt + 1;
    const userMsg: ChatMsg = { id: uid(), role: "user", content };
    set((state) => ({
      chat: [...state.chat, userMsg],
      chatStreaming: true,
      agentStarting: true,
      agentRunId: null,
      agentAttempt: attempt,
      agentPlan: [],
    }));
    try {
      const runId = await Agent.Start(content);
      const current = get();
      if (current.agentAttempt !== attempt || !current.agentStarting || current.workspaceTransitioning) {
        const reportCancellationErrors = canReportWorkspaceDiagnostic(current, workspaceInstanceId);
        agentEventQueues.delete(runId);
        agentEventQueueFailures.delete(runId);
        invokeBestEffort(
          () => Agent.Cancel(runId),
          (error) => {
            if (reportCancellationErrors) reportCancellationFailure(get, workspaceInstanceId, "Agent", error);
          },
        );
        return;
      }
      for (const queuedRunId of agentEventQueues.keys()) {
        if (queuedRunId !== runId) {
          agentEventQueues.delete(queuedRunId);
          agentEventQueueFailures.delete(queuedRunId);
        }
      }
      set({ agentRunId: runId, agentStarting: false });
      get().flushAgentEvents(runId);
    } catch (e) {
      if (get().agentAttempt !== attempt) return;
      agentEventQueues.clear();
      agentEventQueueFailures.clear();
      set((state) => ({
        chat: [...state.chat, { id: uid(), role: "assistant", content: errMessage(e), error: true }],
        chatStreaming: false,
        agentStarting: false,
        agentRunId: null,
        agentAttempt: attempt + 1,
      }));
    }
  },

  approveAgent: async (callId: string, approved: boolean, intentDigest?: string) => {
    // Optimistically resolve the card (which hides its buttons, preventing a
    // double-submit while the call is in flight).
    set((st) => ({
      chat: st.chat.map((m) => (m.callId === callId ? { ...m, approval: approved ? "approved" : "denied" } : m)),
    }));
    try {
      if (intentDigest) await Agent.ApproveIntent(callId, intentDigest, approved);
      else await Agent.Approve(callId, approved);
    } catch (e) {
      // Roll the card back to pending so the user can retry.
      set((st) => ({
        chat: st.chat.map((m) => (m.callId === callId ? { ...m, approval: "pending" } : m)),
      }));
      get().setStatus(errMessage(e), "error");
    }
  },

  handleAgentEvent: (ev: AgentEvent) => {
    const state = get();
    if (ev.runId !== state.agentRunId && !state.agentStarting) return;
    bufferAgentEvent(ev);
    if (ev.runId === state.agentRunId) get().flushAgentEvents(ev.runId);
  },

  flushAgentEvents: (runId: string) => {
    const orderingFailure = agentEventQueueFailures.get(runId);
    if (orderingFailure) {
      const workspaceInstanceId = get().workspaceInstanceId;
      agentEventQueues.delete(runId);
      agentEventQueueFailures.delete(runId);
      invokeBestEffort(
        () => Agent.Cancel(runId),
        (error) => reportCancellationFailure(get, workspaceInstanceId, "Agent", error),
      );
      set((state) => ({
        chat: [
          ...settlePendingAgentCards(state.chat, runId).filter(
            (message) => !(message.role === "assistant" && message.runId === runId),
          ),
          { id: uid(), role: "assistant", content: orderingFailure, error: true },
        ],
        chatStreaming: false,
        agentRunId: null,
        agentStarting: false,
        agentAttempt: state.agentAttempt + 1,
        agentPlan: settleAgentPlan(state.agentPlan),
      }));
      return;
    }
    for (const ev of takeReadyAgentEvents(runId)) {
      if (get().agentRunId !== runId) {
        agentEventQueues.delete(runId);
        agentEventQueueFailures.delete(runId);
        return;
      }
      const match = (m: ChatMsg) => m.callId === ev.callId && m.runId === ev.runId;
    switch (ev.type) {
        case "assistant_text":
          set((st) => ({ chat: [...st.chat, { id: uid(), role: "assistant", content: ev.text ?? "", runId: ev.runId }] }));
        break;
      case "tool_call":
        if (ev.tool === "update_plan") break;
        set((st) => ({
          chat: st.chat.some(match)
            ? st.chat.map((m) => (match(m) ? { ...m, tool: ev.tool, args: ev.args } : m))
            : [
                ...st.chat,
                { id: uid(), role: "tool", content: "", tool: ev.tool, args: ev.args, callId: ev.callId, runId: ev.runId },
              ],
        }));
        break;
      case "approval_request":
        set((st) => {
          const exists = st.chat.some(match);
          return {
            chat: exists
              ? st.chat.map((m) =>
                  match(m)
                    ? {
                        ...m,
                        tool: m.tool ?? ev.tool,
                        args: m.args ?? ev.args,
                        intent: ev.intent,
                        intentDigest: ev.intentDigest,
                        expiresAt: ev.expiresAt,
                        approval: "pending",
                      }
                    : m,
                )
              : [
                  ...st.chat,
                  {
                    id: uid(),
                    role: "tool",
                    content: "",
                    tool: ev.tool,
                    args: ev.args,
                    callId: ev.callId,
                    runId: ev.runId,
                    intent: ev.intent,
                    intentDigest: ev.intentDigest,
                    expiresAt: ev.expiresAt,
                    approval: "pending",
                  },
                ],
          };
        });
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
        if (ev.tool === "update_plan") break;
        set((st) => {
          const exists = st.chat.some(match);
          return {
            chat: exists
              ? st.chat.map((m) =>
                  match(m) ? { ...m, result: ev.result, approval: m.approval === "pending" ? undefined : m.approval } : m,
                )
              : [
                  ...st.chat,
                  { id: uid(), role: "tool", content: "", tool: ev.tool, result: ev.result, callId: ev.callId, runId: ev.runId },
                ],
          };
        });
        break;
      case "plan":
        set({ agentPlan: ev.plan ?? [] });
        break;
        case "done":
          agentEventQueues.delete(runId);
          agentEventQueueFailures.delete(runId);
          set((state) => ({
            chat: settlePendingAgentCards(state.chat, runId),
            chatStreaming: false,
          agentRunId: null,
          agentStarting: false,
          agentAttempt: state.agentAttempt + 1,
          agentPlan: settleAgentPlan(state.agentPlan),
        }));
        return;
        case "error":
          agentEventQueues.delete(runId);
          agentEventQueueFailures.delete(runId);
          set((state) => ({
            chat: [
              ...settlePendingAgentCards(state.chat, runId).filter(
                (message) => !(ev.rolledBack && message.role === "assistant" && message.runId === runId),
              ),
              { id: uid(), role: "assistant", content: ev.text ?? "Agent error", error: true },
            ],
          chatStreaming: false,
          agentRunId: null,
          agentStarting: false,
          agentAttempt: state.agentAttempt + 1,
          agentPlan: settleAgentPlan(state.agentPlan),
        }));
        return;
      }
    }
  },

  sendChat: async (text: string) => {
    const content = text.trim();
    if (!content || !get().isOpen || get().chatStreaming || get().workspaceTransitioning || get().conversationResetting) return;
    const st = get();
    const workspaceInstanceId = st.workspaceInstanceId;
    streamEventQueues.clear();
    streamEventQueueFailures.clear();
    const attempt = st.streamAttempt + 1;
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
    set((state) => ({
      chat: [...state.chat, userMsg, asstMsg],
      chatStreaming: true,
      streamReqId: null,
      streamMsgId: asstMsg.id,
      streamAttempt: attempt,
    }));

    // Ground the answer in the active file (Ask mode), capped.
    const active = st.tabs.find((t) => t.path === st.activePath && t.kind === "file");
    const context = active && !active.binary && !active.tooLarge ? active.content.slice(0, 24000) : "";

    try {
      const reqId = await LLM.Send({ messages: history, context, system: "" });
      const current = get();
      if (
        current.streamAttempt !== attempt ||
        current.streamMsgId !== asstMsg.id ||
        !current.chatStreaming ||
        current.workspaceTransitioning
      ) {
        const reportCancellationErrors = canReportWorkspaceDiagnostic(current, workspaceInstanceId);
        streamEventQueues.delete(reqId);
        streamEventQueueFailures.delete(reqId);
        invokeBestEffort(
          () => LLM.Cancel(reqId),
          (error) => {
            if (reportCancellationErrors) reportCancellationFailure(get, workspaceInstanceId, "Assistant request", error);
          },
        );
        return;
      }
      for (const queuedReqId of streamEventQueues.keys()) {
        if (queuedReqId !== reqId) {
          streamEventQueues.delete(queuedReqId);
          streamEventQueueFailures.delete(queuedReqId);
        }
      }
      set({ streamReqId: reqId });
      get().flushStreamEvents(reqId);
    } catch (e) {
      if (get().streamAttempt !== attempt) return;
      const msg = errMessage(e);
      streamEventQueues.clear();
      streamEventQueueFailures.clear();
      set((state) => ({
        chat: state.chat.map((m) => (m.id === asstMsg.id ? { ...m, content: msg, streaming: false, error: true } : m)),
        chatStreaming: false,
        streamReqId: null,
        streamMsgId: null,
        streamAttempt: attempt + 1,
      }));
    }
  },

  appendDelta: (reqId: string, delta: string, seq?: number) => {
    const state = get();
    if (reqId !== state.streamReqId && !(state.streamReqId === null && state.chatStreaming && state.streamMsgId)) return;
    bufferStreamEvent(reqId, { type: "delta", delta, seq });
    if (reqId === state.streamReqId) get().flushStreamEvents(reqId);
  },

  finishStream: (reqId: string, seq?: number) => {
    const state = get();
    if (reqId !== state.streamReqId && !(state.streamReqId === null && state.chatStreaming && state.streamMsgId)) return;
    bufferStreamEvent(reqId, { type: "done", seq });
    if (reqId === state.streamReqId) get().flushStreamEvents(reqId);
  },

  failStream: (reqId: string, message: string, seq?: number) => {
    const state = get();
    if (reqId !== state.streamReqId && !(state.streamReqId === null && state.chatStreaming && state.streamMsgId)) return;
    bufferStreamEvent(reqId, { type: "error", message, seq });
    if (reqId === state.streamReqId) get().flushStreamEvents(reqId);
  },

  flushStreamEvents: (reqId: string) => {
    const orderingFailure = streamEventQueueFailures.get(reqId);
    if (orderingFailure) {
      const workspaceInstanceId = get().workspaceInstanceId;
      streamEventQueues.delete(reqId);
      streamEventQueueFailures.delete(reqId);
      invokeBestEffort(
        () => LLM.Cancel(reqId),
        (error) => reportCancellationFailure(get, workspaceInstanceId, "Assistant request", error),
      );
      set((state) => ({
        chat: state.chat.map((m) =>
          m.id === state.streamMsgId ? { ...m, content: orderingFailure, streaming: false, error: true } : m,
        ),
        chatStreaming: false,
        streamReqId: null,
        streamMsgId: null,
        streamAttempt: state.streamAttempt + 1,
      }));
      return;
    }
    for (const event of takeReadyStreamEvents(reqId)) {
      const state = get();
      if (state.streamReqId !== reqId || !state.streamMsgId) {
        streamEventQueues.delete(reqId);
        streamEventQueueFailures.delete(reqId);
        return;
      }
      if (event.type === "delta") {
        set({ chat: state.chat.map((m) => (m.id === state.streamMsgId ? { ...m, content: m.content + event.delta } : m)) });
        continue;
      }
      streamEventQueues.delete(reqId);
      streamEventQueueFailures.delete(reqId);
      if (event.type === "done") {
        set((current) => ({
          chat: current.chat.map((m) => (m.id === current.streamMsgId ? { ...m, streaming: false } : m)),
          chatStreaming: false,
          streamReqId: null,
          streamMsgId: null,
          streamAttempt: current.streamAttempt + 1,
        }));
      } else {
        set((current) => ({
          chat: current.chat.map((m) =>
            m.id === current.streamMsgId
              ? { ...m, content: (m.content ? m.content + "\n\n" : "") + "⚠ " + event.message, streaming: false, error: true }
              : m,
          ),
          chatStreaming: false,
          streamReqId: null,
          streamMsgId: null,
          streamAttempt: current.streamAttempt + 1,
        }));
      }
      return;
    }
  },

  cancelChat: () => {
    const state = get();
    const workspaceInstanceId = state.workspaceInstanceId;
    const reportCancellationErrors = canReportWorkspaceDiagnostic(state, workspaceInstanceId);
    if (state.streamReqId) {
      invokeBestEffort(
        () => LLM.Cancel(state.streamReqId!),
        (error) => {
          if (reportCancellationErrors) reportCancellationFailure(get, workspaceInstanceId, "Assistant request", error);
        },
      );
    }
    if (state.agentRunId) {
      invokeBestEffort(
        () => Agent.Cancel(state.agentRunId!),
        (error) => {
          if (reportCancellationErrors) reportCancellationFailure(get, workspaceInstanceId, "Agent", error);
        },
      );
    }
    agentEventQueues.clear();
    agentEventQueueFailures.clear();
    streamEventQueues.clear();
    streamEventQueueFailures.clear();
    set((current) => ({
      chat: current.chat.filter((m) => !(current.agentRunId && m.role === "assistant" && m.runId === current.agentRunId)).map((m) => {
        let nm = m;
        if (m.id === current.streamMsgId) nm = { ...nm, streaming: false };
        // A pending approval can never resolve once the run is cancelled — mark
        // it denied so its tool card doesn't sit "Awaiting approval" forever.
        if (nm.approval === "pending") nm = { ...nm, approval: "denied" };
        return nm;
      }),
      chatStreaming: false,
      streamReqId: null,
      streamMsgId: null,
      streamAttempt: current.streamAttempt + 1,
      agentRunId: null,
      agentStarting: false,
      agentAttempt: current.agentAttempt + 1,
      agentPlan: settleAgentPlan(current.agentPlan),
    }));
  },

  clearChat: () => {
    // Clearing mid-stream must also stop the in-flight run, or the backend keeps
    // streaming into a void and chatStreaming stays stuck (blocking the next send).
    get().cancelChat();
    const resetAttempt = get().conversationResetAttempt + 1;
    set({ chat: [], agentPlan: [], conversationResetting: true, conversationResetAttempt: resetAttempt });
    void Agent.ResetConversation().then(
      () => {
        if (get().conversationResetAttempt === resetAttempt) set({ conversationResetting: false });
      },
      (error) => {
        if (get().conversationResetAttempt !== resetAttempt) return;
        set({ conversationResetting: false });
        get().setStatus(errMessage(error), "error");
      },
    );
  },

  loadModels: async (silent = false) => {
    const attempt = ++modelLoadAttemptCounter;
    const llm = get().settings?.llm;
    const fallbackModels = llm?.provider === "ollama" ? DEFAULT_OLLAMA_MODELS : [];
    const provider = llm?.provider ?? "";
    const baseURL = llm?.baseURL ?? "";
    const isCurrent = () => {
      const current = get().settings?.llm;
      return (
        attempt === modelLoadAttemptCounter &&
        get().settingsError === null &&
        current?.provider === provider &&
        current?.baseURL === baseURL
      );
    };
    const settingsError = get().settingsError;
    if (settingsError) {
      if (!silent) get().setStatus(settingsError, "error");
      return;
    }
    if (!llm?.baseURL) {
      set({ models: fallbackModels });
      return;
    }
    try {
      const m = await LLM.ListModels();
      if (!isCurrent()) return;
      const models = mergeModels(fallbackModels, m);
      set({ models });
      // Auto-select a model on first run so chat works without manual setup.
      const cur = get().settings?.llm.model;
      if (!cur && models.length > 0) await get().saveLLMConfig({ model: models[0] });
    } catch (e) {
      if (!isCurrent()) return;
      set({ models: fallbackModels });
      const cur = get().settings?.llm.model;
      if (!cur && fallbackModels.length > 0) await get().saveLLMConfig({ model: fallbackModels[0] });
      if (!silent) get().setStatus(errMessage(e), "error");
    }
  },

  saveLLMConfig: async (patch: Partial<LLMConfig>) => {
    const cur = get().settings;
    if (!cur) return;
    if (get().settingsError) {
      get().setStatus(get().settingsError!, "error");
      return;
    }
    const normalizedPatch = { ...patch };
    if (normalizedPatch.baseURL !== undefined) normalizedPatch.baseURL = stripUrlCreds(normalizedPatch.baseURL);
    const next = { ...cur, llm: { ...cur.llm, ...normalizedPatch } };
    const mutation = claimSettingsMutation();
    set({ settings: next });
    try {
      const loaded = await enqueueSettingsMutation(async () => {
        // Merge this domain patch into the state that actually precedes it in
        // the backend queue. This preserves recents and other domains changed
        // while the optimistic renderer update was in flight.
        const base = await Settings.Load();
        const candidate = { ...base, llm: { ...base.llm, ...normalizedPatch } };
        await Settings.Save(candidate);
        return loadAuthoritativeSettingsState();
      });
      if (mutation === latestSettingsMutation) {
        set({
          settings: loaded.settings,
          settingsError: loaded.settingsError,
          llmAPIKeyStatus: loaded.llmAPIKeyStatus,
          llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
          llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
          recents: loaded.settings.recentWorkspaces ?? [],
        });
        if (loaded.settingsError) get().setStatus(loaded.settingsError, "error");
      }
    } catch (e) {
      if (mutation === latestSettingsMutation) {
        // Restore the backend-authoritative snapshot on rejection so controls
        // never keep displaying a value that was not persisted.
        try {
          const loaded = await enqueueSettingsMutation(loadAuthoritativeSettingsState);
          if (mutation === latestSettingsMutation) {
            set({
              settings: loaded.settings,
              settingsError: loaded.settingsError,
              llmAPIKeyStatus: loaded.llmAPIKeyStatus,
              llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
              llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
              recents: loaded.settings.recentWorkspaces ?? [],
            });
          }
        } catch {
          if (get().settings === next) set({ settings: cur });
        }
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  saveAgentConfig: async (patch: Partial<AgentConfig>) => {
    const cur = get().settings;
    if (!cur) return;
    if (get().settingsError) {
      get().setStatus(get().settingsError!, "error");
      return;
    }
    const current = agentConfigFromSettings(cur);
    const agent = clampAgentConfig(patch, current);
    const next = { ...cur, agent } as SettingsModel & { agent: AgentConfig };
    const mutation = claimSettingsMutation();
    set({ settings: next });
    try {
      const loaded = await enqueueSettingsMutation(async () => {
        const base = await Settings.Load();
        const candidate = {
          ...base,
          agent: clampAgentConfig(patch, agentConfigFromSettings(base)),
        } as SettingsModel & { agent: AgentConfig };
        await Settings.Save(candidate as SettingsModel);
        return loadAuthoritativeSettingsState();
      });
      if (mutation === latestSettingsMutation) {
        set({
          settings: loaded.settings,
          settingsError: loaded.settingsError,
          llmAPIKeyStatus: loaded.llmAPIKeyStatus,
          llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
          llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
          recents: loaded.settings.recentWorkspaces ?? [],
        });
      }
    } catch (e) {
      if (mutation === latestSettingsMutation) {
        try {
          const loaded = await enqueueSettingsMutation(loadAuthoritativeSettingsState);
          if (mutation === latestSettingsMutation) {
            set({
              settings: loaded.settings,
              settingsError: loaded.settingsError,
              llmAPIKeyStatus: loaded.llmAPIKeyStatus,
              llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
              llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
              recents: loaded.settings.recentWorkspaces ?? [],
            });
          }
        } catch {
          if (get().settings === next) set({ settings: cur });
        }
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  saveEditorConfig: async (patch: Partial<EditorSettings>) => {
    const cur = get().settings;
    if (!cur) return;
    if (get().settingsError) {
      get().setStatus(get().settingsError!, "error");
      return;
    }
    // Clamp numeric prefs so an out-of-range value can't be persisted regardless
    // of how it was entered (the inputs' min/max attributes are only advisory).
    const clamped: Partial<EditorSettings> = { ...patch };
    if (clamped.fontSize !== undefined) clamped.fontSize = Math.max(9, Math.min(28, Math.round(clamped.fontSize)));
    if (clamped.tabSize !== undefined) clamped.tabSize = Math.max(1, Math.min(8, Math.round(clamped.tabSize)));
    const next = { ...cur, editor: { ...cur.editor, ...clamped } };
    const mutation = claimSettingsMutation();
    set({ settings: next });
    try {
      const loaded = await enqueueSettingsMutation(async () => {
        const base = await Settings.Load();
        const candidate = { ...base, editor: { ...base.editor, ...clamped } };
        await Settings.Save(candidate);
        return loadAuthoritativeSettingsState();
      });
      if (mutation === latestSettingsMutation) {
        set({
          settings: loaded.settings,
          settingsError: loaded.settingsError,
          llmAPIKeyStatus: loaded.llmAPIKeyStatus,
          llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
          llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
          recents: loaded.settings.recentWorkspaces ?? [],
        });
      }
    } catch (e) {
      if (mutation === latestSettingsMutation) {
        try {
          const loaded = await enqueueSettingsMutation(loadAuthoritativeSettingsState);
          if (mutation === latestSettingsMutation) {
            set({
              settings: loaded.settings,
              settingsError: loaded.settingsError,
              llmAPIKeyStatus: loaded.llmAPIKeyStatus,
              llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
              llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
              recents: loaded.settings.recentWorkspaces ?? [],
            });
          }
        } catch {
          if (get().settings === next) set({ settings: cur });
        }
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  setUIFontSize: async (n: number) => {
    const size = Math.max(10, Math.min(20, Math.round(n)));
    const cur = get().settings;
    if (!cur) return;
    if (get().settingsError) {
      get().setStatus(get().settingsError!, "error");
      return;
    }
    const next = { ...cur, uiFontSize: size };
    const mutation = claimSettingsMutation();
    set({ settings: next });
    applyUIFont(size);
    try {
      const loaded = await enqueueSettingsMutation(async () => {
        const base = await Settings.Load();
        await Settings.Save({ ...base, uiFontSize: size });
        return loadAuthoritativeSettingsState();
      });
      if (mutation === latestSettingsMutation) {
        set({
          settings: loaded.settings,
          settingsError: loaded.settingsError,
          llmAPIKeyStatus: loaded.llmAPIKeyStatus,
          llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
          llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
          recents: loaded.settings.recentWorkspaces ?? [],
        });
        applyUIFont(loaded.settings.uiFontSize);
      }
    } catch (e) {
      if (mutation === latestSettingsMutation) {
        try {
          const loaded = await enqueueSettingsMutation(loadAuthoritativeSettingsState);
          if (mutation === latestSettingsMutation) {
            set({
              settings: loaded.settings,
              settingsError: loaded.settingsError,
              llmAPIKeyStatus: loaded.llmAPIKeyStatus,
              llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
              llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
              recents: loaded.settings.recentWorkspaces ?? [],
            });
            applyUIFont(loaded.settings.uiFontSize);
          }
        } catch {
          if (get().settings === next) {
            set({ settings: cur });
            applyUIFont(cur.uiFontSize);
          }
        }
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  setApiKey: async (value: string) => {
    if (get().settingsError) {
      get().setStatus(get().settingsError!, "error");
      return false;
    }
    const mutation = claimSettingsMutation();
    try {
      const loaded = await enqueueSettingsMutation(async () => {
        if (value.trim()) await SecretService.SetKey(API_KEY_REF, value);
        else await SecretService.DeleteKey(API_KEY_REF);
        return loadAuthoritativeSettingsState();
      });
      if (mutation === latestSettingsMutation) {
        set({
          settings: loaded.settings,
          settingsError: loaded.settingsError,
          llmAPIKeyStatus: loaded.llmAPIKeyStatus,
          llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
          llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
          recents: loaded.settings.recentWorkspaces ?? [],
        });
        get().setStatus(value.trim() ? "API key saved" : "Stored API key removed", "success");
      }
      return true;
    } catch (e) {
      if (mutation === latestSettingsMutation) {
        try {
          const loaded = await enqueueSettingsMutation(loadAuthoritativeSettingsState);
          if (mutation === latestSettingsMutation) {
            set({
              settings: loaded.settings,
              settingsError: loaded.settingsError,
              llmAPIKeyStatus: loaded.llmAPIKeyStatus,
              llmAPIKeyStatusMessage: loaded.llmAPIKeyStatusMessage,
              llmAPIKeyAvailable: loaded.llmAPIKeyAvailable,
              recents: loaded.settings.recentWorkspaces ?? [],
            });
          }
        } catch {
          // Keep the prior renderer state; the mutation error is still surfaced.
        }
        get().setStatus(errMessage(e), "error");
      }
      return false;
    }
  },

  loadDbProfiles: async () => {
    try {
      const loaded = await loadAuthoritativeDatabaseState();
      set({ dbProfiles: loaded.profiles, dbCredentialStatuses: loaded.credentialStatuses, dbError: loaded.loadError });
      if (loaded.loadError) get().setStatus(loaded.loadError, "error");
      else if (loaded.credentialStatusError) get().setStatus(loaded.credentialStatusError, "error");
    } catch (e) {
      // Retain the last visible profile list, but discard all credential claims.
      set({ dbCredentialStatuses: {}, dbError: "Saved database connections could not be refreshed. Profile changes are disabled." });
      get().setStatus(errMessage(e), "error");
    }
  },

  saveDbProfile: (p: DbProfile) => {
    if (!p.id) {
      const existing = inFlightDbCreates.get(p);
      if (existing) return existing;
    }
    const operation = enqueueDbMutation(async () => {
      if (get().dbError) {
        get().setStatus(get().dbError!, "error");
        return null;
      }
      try {
        const outcome = await Db.SaveProfileReconciled(p);
        await get().loadDbProfiles();
        if (outcome.finalizationWarning) get().setStatus(outcome.finalizationWarning, "error");
        return outcome.profile;
      } catch (e) {
        // Re-read even after rejection. This reconciles a forward commit from
        // an older backend instead of leaving a retryable empty-id draft.
        await get().loadDbProfiles();
        get().setStatus(errMessage(e), "error");
        return null;
      }
    });
    if (p.id) return operation;
    const tracked = operation.finally(() => {
      if (inFlightDbCreates.get(p) === tracked) inFlightDbCreates.delete(p);
    });
    inFlightDbCreates.set(p, tracked);
    return tracked;
  },

  deleteDbProfile: async (id: string) => {
    return enqueueDbMutation(async () => {
      if (get().dbError) {
        get().setStatus(get().dbError!, "error");
        return;
      }
      try {
        const outcome = await Db.DeleteProfileReconciled(id);
        if (!outcome.deleted) throw new Error("The database connection was not deleted.");
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
        if (outcome.finalizationWarning) get().setStatus(outcome.finalizationWarning, "error");
      } catch (e) {
        await get().loadDbProfiles();
        get().setStatus(errMessage(e), "error");
      }
    });
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
    const generation = ++dbSelectionGeneration;
    latestDbPreviewRequest = ++dbPreviewRequestCounter;
    set({ activeDbId: id, dbTables: [], dbTablesLoading: true });
    try {
      const tables = await Db.ListTables(id);
      if (dbSelectionGeneration === generation && get().activeDbId === id) {
        set({ dbTables: tables, dbTablesLoading: false });
      }
    } catch (e) {
      if (dbSelectionGeneration === generation && get().activeDbId === id) {
        set({ dbTablesLoading: false });
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  openDbTable: async (id: string, table: DbTable) => {
    if (get().activeDbId !== id) return;
    const selectionGeneration = dbSelectionGeneration;
    const previewRequest = ++dbPreviewRequestCounter;
    latestDbPreviewRequest = previewRequest;
    try {
      // Identifier quoting belongs to the backend because it owns the profile's
      // SQL dialect. The renderer passes schema/table identity as data only.
      const sql = await Db.BuildTableQuery(id, table.schema, table.name, 100);
      if (
        dbSelectionGeneration !== selectionGeneration ||
        latestDbPreviewRequest !== previewRequest ||
        get().activeDbId !== id
      ) {
        return;
      }
      get().setDbSql(id, sql);
      get().openDbQuery(id);
    } catch (e) {
      if (
        dbSelectionGeneration === selectionGeneration &&
        latestDbPreviewRequest === previewRequest &&
        get().activeDbId === id
      ) {
        get().setStatus(errMessage(e), "error");
      }
    }
  },

  openDbQuery: (id: string) => {
    const key = `db:${id}`;
    const st = get();
    if (!st.tabs.find((t) => t.path === key)) {
      const prof = st.dbProfiles.find((p) => p.id === id);
      const tab: Tab = {
        instanceId: nextTabInstanceId(),
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
    const requested = get();
    const op = requested.pendingFileOp;
    set({ pendingFileOp: null });
    if (!op) return;
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const requestedFileGeneration = workspaceFileStateGeneration;
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
        if (knownRenameDestination(requested, op.targetRel, newRel)) {
          get().setStatus(`${newRel} already exists. Choose a different name.`, "error");
          return;
        }
        if (affectedTabs(requested, op.targetRel).some((tab) => tab.kind === "file" && tab.tooLarge)) {
          get().setStatus("Close affected large-file views before renaming this path.", "error");
          return;
        }

        const releaseMutation = claimFileMutationPath(workspaceInstanceId, op.targetRel);
        try {
          await enqueueFileOperation(async () => {
            const isCurrent = () => canReportWorkspaceDiagnostic(get(), workspaceInstanceId);
            if (!isCurrent()) return;
            if (workspaceFileStateGeneration !== requestedFileGeneration) {
              get().setStatus("The workspace files changed before rename started. Retry from the refreshed tree.", "error");
              return;
            }
            const beforeRename = get();
            if (knownRenameDestination(beforeRename, op.targetRel, newRel)) {
              get().setStatus(`${newRel} already exists. Choose a different name.`, "error");
              return;
            }
            if (affectedTabs(beforeRename, op.targetRel).some((tab) => tab.kind === "file" && tab.tooLarge)) {
              get().setStatus("Close affected large-file views before renaming this path.", "error");
              return;
            }

            await waitForTabSaves(workspaceInstanceId, op.targetRel);
            if (!isCurrent() || workspaceFileStateGeneration !== requestedFileGeneration) return;

            try {
              await Workspace.Rename(op.targetRel, newRel);
              if (!isCurrent()) return;
              invalidateDirectoryLoads(workspaceInstanceId, op.targetRel);
              recordFileStateMutation(op.targetRel, newRel);
              set((state) =>
                canReportWorkspaceDiagnostic(state, workspaceInstanceId)
                  ? reconcileRenameState(state, op.targetRel, newRel)
                  : {},
              );
            } catch (renameError) {
              if (!isCurrent()) return;
              let entries: Entry[] | null = null;
              try {
                entries = await Workspace.ListDir(parent);
              } catch {
                // Preserve the original mutation diagnostic below.
              }
              if (!isCurrent()) return;
              if (entries) {
                set((state) => ({ childrenByPath: { ...state.childrenByPath, [parent]: entries as Entry[] } }));
                const oldPresent = entries.some((entry) => entry.path === op.targetRel);
                const newPresent = entries.some((entry) => entry.path === newRel);
                if (!oldPresent && newPresent) {
                  invalidateDirectoryLoads(workspaceInstanceId, op.targetRel);
                  recordFileStateMutation(op.targetRel, newRel);
                  set((state) => reconcileRenameState(state, op.targetRel, newRel));
                  get().syncWatches();
                  void get().loadGitStatus();
                  get().setStatus(`Rename completed, but confirmation failed: ${errMessage(renameError)}`, "error");
                  return;
                }
              }
              get().setStatus(errMessage(renameError), "error");
              return;
            }

            get().syncWatches();
            await get().loadChildren(parent);
            void get().loadGitStatus();
          });
        } finally {
          releaseMutation();
        }
        return;
      }

      const rel = op.targetRel ? `${op.targetRel}/${name}` : name;
      await enqueueFileOperation(async () => {
        const isCurrent = () => canReportWorkspaceDiagnostic(get(), workspaceInstanceId);
        if (!isCurrent()) return;
        if (workspaceFileStateGeneration !== requestedFileGeneration) {
          get().setStatus("The workspace files changed before creation started. Retry from the refreshed tree.", "error");
          return;
        }
        if (op.type === "newFile") await Workspace.CreateFile(rel);
        else await Workspace.CreateDir(rel);
        if (!isCurrent()) return;
        recordFileStateMutation(rel);
        set((state) => ({ expanded: { ...state.expanded, [op.targetRel]: true } }));
        await get().loadChildren(op.targetRel);
        if (op.type === "newFile") await get().openFile(rel, name);
        void get().loadGitStatus();
      });
    } catch (e) {
      if (!canReportWorkspaceDiagnostic(get(), workspaceInstanceId)) return;
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
    const requested = get();
    const pd = requested.pendingDelete;
    set({ pendingDelete: null });
    if (!pd) return;
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const affected = affectedTabs(requested, pd.rel);
    if (affected.some(isEditableDirtyTab)) {
      get().setStatus("Save or close affected unsaved editors before deleting this path.", "error");
      return;
    }
    if (affected.some((tab) => tab.kind === "file" && tab.tooLarge)) {
      get().setStatus("Close affected large-file views before deleting this path.", "error");
      return;
    }
    const workspaceInstanceId = requested.workspaceInstanceId;
    const requestedFileGeneration = workspaceFileStateGeneration;
    const releaseMutation = claimFileMutationPath(workspaceInstanceId, pd.rel);
    try {
      await enqueueFileOperation(async () => {
        const isCurrent = () => canReportWorkspaceDiagnostic(get(), workspaceInstanceId);
        if (!isCurrent()) return;
        if (workspaceFileStateGeneration !== requestedFileGeneration) {
          get().setStatus("The workspace files changed before deletion started. Retry from the refreshed tree.", "error");
          return;
        }
        await waitForTabSaves(workspaceInstanceId, pd.rel);
        if (!isCurrent() || workspaceFileStateGeneration !== requestedFileGeneration) return;
        const beforeDelete = affectedTabs(get(), pd.rel);
        if (beforeDelete.some(isEditableDirtyTab)) {
          get().setStatus("Deletion stopped because an affected editor now has unsaved changes.", "error");
          return;
        }
        if (beforeDelete.some((tab) => tab.kind === "file" && tab.tooLarge)) {
          get().setStatus("Close affected large-file views before deleting this path.", "error");
          return;
        }

        const parent = dirOf(pd.rel);
        try {
          await Workspace.Delete(pd.rel);
          if (!isCurrent()) return;
          invalidateDirectoryLoads(workspaceInstanceId, pd.rel);
          recordFileStateMutation(pd.rel);
          let preservedDirtyTabs = 0;
          set((state) => {
            if (!canReportWorkspaceDiagnostic(state, workspaceInstanceId)) return {};
            const reconciled = reconcileDeleteState(state, pd.rel);
            preservedDirtyTabs = reconciled.preservedDirtyTabs;
            return reconciled.patch;
          });
          if (preservedDirtyTabs > 0) {
            get().setStatus(
              "Delete completed, but edits made while it was running were kept as unsaved drafts. Saving will recreate those files.",
              "error",
            );
          }
        } catch (deleteError) {
          if (!isCurrent()) return;
          let entries: Entry[] | null = null;
          try {
            entries = await Workspace.ListDir(parent);
          } catch {
            // Preserve the original mutation diagnostic below.
          }
          if (!isCurrent()) return;
          if (entries) {
            set((state) => ({ childrenByPath: { ...state.childrenByPath, [parent]: entries as Entry[] } }));
            if (!entries.some((entry) => entry.path === pd.rel)) {
              invalidateDirectoryLoads(workspaceInstanceId, pd.rel);
              recordFileStateMutation(pd.rel);
              let preservedDirtyTabs = 0;
              set((state) => {
                const reconciled = reconcileDeleteState(state, pd.rel);
                preservedDirtyTabs = reconciled.preservedDirtyTabs;
                return reconciled.patch;
              });
              get().syncWatches();
              void get().loadGitStatus();
              get().setStatus(
                preservedDirtyTabs > 0
                  ? `Delete completed, but confirmation failed: ${errMessage(deleteError)} Edits made while it was running were kept as unsaved drafts.`
                  : `Delete completed, but confirmation failed: ${errMessage(deleteError)}`,
                "error",
              );
              return;
            }
          }
          get().setStatus(errMessage(deleteError), "error");
          return;
        }

        get().syncWatches();
        await get().loadChildren(parent);
        void get().loadGitStatus();
      });
    } catch (e) {
      if (canReportWorkspaceDiagnostic(get(), workspaceInstanceId)) get().setStatus(errMessage(e), "error");
      // Reconcile in case the entry was already gone (stale tree node).
      if (canReportWorkspaceDiagnostic(get(), workspaceInstanceId)) void get().loadChildren(dirOf(pd.rel));
    } finally {
      releaseMutation();
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
    const requested = get();
    if (!requested.isOpen || requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const fileGeneration = workspaceFileStateGeneration;
    const isCurrentSearch = () => {
      const current = get();
      return (
        current.workspaceInstanceId === workspaceInstanceId &&
        !current.workspaceTransitioning &&
        current.searchQuery === query
      );
    };
    set({ searching: true });
    try {
      const r = await Workspace.Search(q, false);
      // A path mutation locally remaps the previous snapshot. Never replace it
      // with a pre-mutation backend result that still contains old paths.
      if (isCurrentSearch() && workspaceFileStateGeneration === fileGeneration) set({ searchResults: r });
    } catch (e) {
      if (isCurrentSearch() && workspaceFileStateGeneration === fileGeneration) get().setStatus(errMessage(e), "error");
    } finally {
      if (isCurrentSearch()) set({ searching: false });
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
    const workspaceInstanceId = get().workspaceInstanceId;
    if (!get().isOpen || get().workspaceTransitioning) {
      set({ artifacts: [], artifactsError: null });
      return;
    }
    try {
      const artifacts = await Artifacts.ListArtifacts();
      const st = get();
      if (st.workspaceInstanceId === workspaceInstanceId && !st.workspaceTransitioning) {
        set({ artifacts, artifactsError: null });
      }
    } catch (e) {
      const st = get();
      if (st.workspaceInstanceId === workspaceInstanceId && !st.workspaceTransitioning) {
        const message = errMessage(e);
        // Never retain actionable rows from an earlier successful load after
        // the registry has failed integrity validation.
        set({ artifacts: [], artifactsError: message });
        st.setStatus(message, "error");
      }
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
    const resource = activeResourceCapabilities(get().tabs, get().activePath);
    const tab = resource.resource;
    if (!resource.realFile || !resource.path || !tab) {
      get().setStatus("Open a file to save it as an artifact", "error");
      return;
    }
    try {
      await Artifacts.CreateArtifact({
        id: "",
        kind,
        title: tab.name,
        path: resource.path,
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
    const requested = get();
    if (!requested.isOpen) {
      set({ diagnostics: null });
      return;
    }
    if (requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const fileGeneration = workspaceFileStateGeneration;
    const isCurrent = () => canReportWorkspaceDiagnostic(get(), workspaceInstanceId);
    set({ loadingDiagnostics: true });
    try {
      const d = await Workspace.Diagnostics();
      if (isCurrent() && workspaceFileStateGeneration === fileGeneration) set({ diagnostics: d });
    } catch (e) {
      if (isCurrent() && workspaceFileStateGeneration === fileGeneration) get().setStatus(errMessage(e), "error");
    } finally {
      if (isCurrent()) set({ loadingDiagnostics: false });
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
    const requested = get();
    if (!requested.isOpen) {
      set({ allFiles: [], loadingAllFiles: false, allFilesError: null });
      return;
    }
    if (requested.workspaceTransitioning) return;
    const workspaceInstanceId = requested.workspaceInstanceId;
    const fileGeneration = workspaceFileStateGeneration;
    const isCurrent = () => canReportWorkspaceDiagnostic(get(), workspaceInstanceId);
    set({ loadingAllFiles: true, allFilesError: null });
    try {
      const files = await Workspace.ListAllFiles();
      if (isCurrent() && workspaceFileStateGeneration === fileGeneration) {
        set({ allFiles: files, loadingAllFiles: false });
      }
    } catch (e) {
      if (isCurrent() && workspaceFileStateGeneration === fileGeneration) {
        set({ loadingAllFiles: false, allFilesError: errMessage(e) });
        get().setStatus(errMessage(e), "error");
      }
    } finally {
      if (isCurrent() && workspaceFileStateGeneration !== fileGeneration) set({ loadingAllFiles: false });
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
