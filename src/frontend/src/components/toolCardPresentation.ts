export interface ToolCardPresentation {
  summary: string;
  prompt: string;
  detail: string | null;
  argumentError: string | null;
  approvalSafe: boolean;
}

type ToolArguments = Record<string, unknown>;

const DISPLAY_STRING_FIELDS = [
  "url",
  "command",
  "path",
  "outPath",
  "connectionId",
  "table",
  "sql",
  "query",
  "method",
  "body",
  "content",
  "oldText",
  "newText",
] as const;

function parseToolArguments(raw: string | undefined): { args: ToolArguments; error: string | null } {
  if (!raw?.trim()) return { args: {}, error: null };
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { args: {}, error: "Tool arguments are not valid JSON. Approval is disabled." };
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { args: {}, error: "Tool arguments must be a JSON object. Approval is disabled." };
  }

  const args = parsed as ToolArguments;
  const invalidFields = DISPLAY_STRING_FIELDS.filter(
    (field) => args[field] !== undefined && typeof args[field] !== "string",
  );
  const error = invalidFields.length
    ? `Tool arguments contain non-string display fields (${invalidFields.join(", ")}). Approval is disabled.`
    : null;
  return { args, error };
}

function textArgument(args: ToolArguments, name: string): string | undefined {
  const value = args[name];
  return typeof value === "string" ? value : undefined;
}

export function toolCardPresentation(toolName: string, rawArgs: string | undefined): ToolCardPresentation {
  const { args, error: argumentError } = parseToolArguments(rawArgs);
  const argument = (name: string) => textArgument(args, name);
  const query = argument("query");
  const summary =
    ["url", "command", "path", "outPath", "connectionId", "table", "sql"]
      .map(argument)
      .find((value) => value !== undefined) ?? (query ? `"${query}"` : "");

  let prompt = "Allow this action?";
  let detail: string | null = null;
  if (toolName === "run_command") {
    prompt = "Run this shell command in the workspace?";
    detail = argument("command") ?? "";
  } else if (toolName === "http_request") {
    prompt = "Send this HTTP request?";
    detail = `${argument("method") || "GET"} ${argument("url") || ""}`;
    const body = argument("body");
    if (body) detail += `\nRequest body: ${body.length.toLocaleString()} characters (shown in full below)`;
  } else if (toolName === "db_query") {
    prompt = "Run this database query?";
    detail = argument("sql") ?? "";
  } else if (toolName === "write_file") {
    prompt = `Create or overwrite ${argument("path") ?? "this file"}?`;
    const content = argument("content") ?? "";
    detail = `${content.length.toLocaleString()} characters (shown in full below)`;
  } else if (toolName === "apply_edit") {
    prompt = `Apply this edit to ${argument("path") ?? "this file"}?`;
    const oldText = argument("oldText") ?? "";
    const newText = argument("newText") ?? "";
    detail = `Replace ${oldText.length.toLocaleString()} characters with ${newText.length.toLocaleString()} characters (shown in full below)`;
  }

  // Do not show a plausible-but-inaccurate preview when fields used by the card
  // could not be interpreted. The canonical intent remains visible for diagnosis.
  if (argumentError) detail = null;
  return {
    summary,
    prompt,
    detail,
    argumentError,
    approvalSafe: argumentError === null,
  };
}
