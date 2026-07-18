import { describe, expect, it } from "vitest";
import { visibleAssistantContent } from "./assistantContent";

describe("visibleAssistantContent", () => {
  it.each([
    "analysis\nThis is a legitimate report section.",
    "The user denied the write_file request in this documented example.",
    "Wait, I see what happened: the parser retained the marker.",
    "A protocol article may mention <channel|> and <|thought verbatim.",
    "Continue now by emitting real tool_calls is quoted test data.",
    "The prompt says tool output should remain visible.",
  ])("preserves provider text that happens to contain an old heuristic trigger", (content) => {
    expect(visibleAssistantContent(`\r\n${content}\r\n`)).toBe(content);
  });
});
