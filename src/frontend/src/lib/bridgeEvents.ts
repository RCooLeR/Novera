import type { AgentEvent, PlanStatus, PlanStep } from "../state/store";
import type { AppMenuAction } from "./menuActions";

function completeMenuActions<const T extends readonly AppMenuAction[]>(
  actions: Exclude<AppMenuAction, T[number]> extends never ? T : never,
): T {
  return actions;
}

const MENU_ACTIONS = new Set<AppMenuAction>(
  completeMenuActions([
    "open_folder",
    "new_file",
    "new_folder",
    "save",
    "quit",
    "edit_undo",
    "edit_redo",
    "edit_cut",
    "edit_copy",
    "edit_paste",
    "edit_select_all",
    "format_document",
    "palette",
    "quickopen",
    "view_explorer",
    "view_search",
    "view_git",
    "view_db",
    "view_artifacts",
    "view_settings",
    "view_problems",
    "toggle_sidebar",
    "toggle_panel",
    "toggle_assistant",
    "tool_csv_schema",
    "tool_csv_to_sql",
    "tool_dump_analyze",
    "tool_clean_dump",
    "tool_data_tools",
    "tool_save_artifact",
    "help_about",
    "help_docs",
    "help_issues",
  ] as const),
);
const AGENT_EVENT_TYPES = new Set([
  "assistant_text",
  "tool_call",
  "approval_request",
  "continue_request",
  "tool_result",
  "plan",
  "done",
  "error",
]);
const PLAN_STATUSES = new Set<PlanStatus>(["todo", "in_progress", "done"]);

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

export function eventPayload(data: unknown): unknown {
  return Array.isArray(data) ? data[0] : data;
}

function optionalString(value: unknown): value is string | undefined {
  return value === undefined || typeof value === "string";
}

function optionalBoolean(value: unknown): value is boolean | undefined {
  return value === undefined || typeof value === "boolean";
}

function optionalSequence(value: unknown): value is number | undefined {
  return value === undefined || (typeof value === "number" && Number.isSafeInteger(value) && value > 0);
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function plan(value: unknown): PlanStep[] | null {
  if (value === undefined) return [];
  if (!Array.isArray(value)) return null;
  const result: PlanStep[] = [];
  for (const item of value) {
    const entry = record(item);
    if (!entry || !nonEmptyString(entry.title) || typeof entry.status !== "string" || !PLAN_STATUSES.has(entry.status as PlanStatus)) {
      return null;
    }
    result.push({ title: entry.title, status: entry.status as PlanStatus });
  }
  return result;
}

export interface LLMEventPayload {
  id: string;
  seq?: number;
  delta?: string;
  message?: string;
}

export interface BigFileJobStart {
  [key: string]: unknown;
  id: string;
  sequence: number;
  title: string;
  kind: string;
  fileId: string;
  completed: number;
  total: number;
  records: number;
  note: string;
}

export type BigFileJobProgress = BigFileJobStart;

export interface BigFileJobEnd extends BigFileJobStart {
  status: "completed" | "failed" | "cancelled" | "panicked";
}

export function parseLLMEvent(data: unknown): LLMEventPayload | null {
  const value = record(eventPayload(data));
  if (
    !value ||
    typeof value.id !== "string" ||
    value.id.length === 0 ||
    !optionalSequence(value.seq) ||
    !optionalString(value.delta) ||
    !optionalString(value.message)
  ) {
    return null;
  }
  const result: LLMEventPayload = { id: value.id };
  if (typeof value.seq === "number") result.seq = value.seq;
  if (typeof value.delta === "string") result.delta = value.delta;
  if (typeof value.message === "string") result.message = value.message;
  return result;
}

export function parseBigFileJobStart(data: unknown): BigFileJobStart | null {
  const value = record(eventPayload(data));
  return parseBigFileJobPayload(value);
}

export function parseBigFileJobProgress(data: unknown): BigFileJobProgress | null {
  const value = record(eventPayload(data));
  return parseBigFileJobPayload(value);
}

export function parseBigFileJobEnd(data: unknown): BigFileJobEnd | null {
  const value = record(eventPayload(data));
  const payload = parseBigFileJobPayload(value);
  if (
    !payload ||
    !value ||
    (value.status !== "completed" && value.status !== "failed" && value.status !== "cancelled" && value.status !== "panicked")
  ) {
    return null;
  }
  return { ...payload, status: value.status };
}

function parseBigFileJobPayload(value: Record<string, unknown> | null): BigFileJobStart | null {
  if (
    !value ||
    !nonEmptyString(value.id) ||
    typeof value.sequence !== "number" ||
    !Number.isSafeInteger(value.sequence) ||
    value.sequence <= 0 ||
    !nonEmptyString(value.title) ||
    !nonEmptyString(value.kind) ||
    typeof value.fileId !== "string" ||
    !nonNegativeSafeInteger(value.completed) ||
    !nonNegativeSafeInteger(value.total) ||
    !nonNegativeSafeInteger(value.records) ||
    typeof value.note !== "string"
  ) {
    return null;
  }
  return {
    id: value.id,
    sequence: value.sequence,
    title: value.title,
    kind: value.kind,
    fileId: value.fileId,
    completed: value.completed,
    total: value.total,
    records: value.records,
    note: value.note,
  };
}

function nonNegativeSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function hasRequiredAgentFields(value: Record<string, unknown>): boolean {
  switch (value.type) {
    case "assistant_text":
    case "error":
      return nonEmptyString(value.text);
    case "tool_call":
      return nonEmptyString(value.callId) && nonEmptyString(value.tool) && typeof value.args === "string";
    case "approval_request":
      return (
        nonEmptyString(value.callId) &&
        nonEmptyString(value.tool) &&
        typeof value.args === "string" &&
        nonEmptyString(value.intent) &&
        nonEmptyString(value.intentDigest) &&
        nonEmptyString(value.expiresAt)
      );
    case "continue_request":
      return nonEmptyString(value.callId) && nonEmptyString(value.text);
    case "tool_result":
      return nonEmptyString(value.callId) && nonEmptyString(value.tool) && typeof value.result === "string";
    case "plan":
      return Array.isArray(value.plan);
    case "done":
      return true;
    default:
      return false;
  }
}

export function parseAgentEvent(data: unknown): AgentEvent | null {
  const value = record(eventPayload(data));
  if (
    !value ||
    typeof value.runId !== "string" ||
    value.runId.length === 0 ||
    typeof value.type !== "string" ||
    !AGENT_EVENT_TYPES.has(value.type) ||
    !optionalSequence(value.seq) ||
    !optionalBoolean(value.canceled) ||
    !optionalBoolean(value.rolledBack) ||
    !optionalString(value.text) ||
    !optionalString(value.callId) ||
    !optionalString(value.tool) ||
    !optionalString(value.args) ||
    !optionalString(value.intent) ||
    !optionalString(value.intentDigest) ||
    !optionalString(value.expiresAt) ||
    !optionalString(value.result) ||
    !hasRequiredAgentFields(value)
  ) {
    return null;
  }
  const parsedPlan = plan(value.plan);
  if (parsedPlan === null) return null;
  const result: AgentEvent = { runId: value.runId, type: value.type };
  if (typeof value.seq === "number") result.seq = value.seq;
  if (typeof value.canceled === "boolean") result.canceled = value.canceled;
  if (typeof value.rolledBack === "boolean") result.rolledBack = value.rolledBack;
  if (typeof value.text === "string") result.text = value.text;
  if (typeof value.callId === "string") result.callId = value.callId;
  if (typeof value.tool === "string") result.tool = value.tool;
  if (typeof value.args === "string") result.args = value.args;
  if (typeof value.intent === "string") result.intent = value.intent;
  if (typeof value.intentDigest === "string") result.intentDigest = value.intentDigest;
  if (typeof value.expiresAt === "string") result.expiresAt = value.expiresAt;
  if (typeof value.result === "string") result.result = value.result;
  if (value.plan !== undefined) result.plan = parsedPlan;
  return result;
}

export function parseChangedPath(data: unknown): string | null {
  const value = record(eventPayload(data));
  return value && typeof value.path === "string" && value.path.length > 0 ? value.path : null;
}

export function parseErrorMessage(data: unknown): string | null {
  const value = record(eventPayload(data));
  return value && typeof value.message === "string" && value.message.length > 0 ? value.message : null;
}

export function parseMenuAction(data: unknown): AppMenuAction | null {
  const value = eventPayload(data);
  return typeof value === "string" && MENU_ACTIONS.has(value as AppMenuAction) ? (value as AppMenuAction) : null;
}
