import { describe, expect, it } from "vitest";
import {
  parseAgentEvent,
  parseBigFileJobEnd,
  parseBigFileJobProgress,
  parseBigFileJobStart,
  parseChangedPath,
  parseErrorMessage,
  parseLLMEvent,
  parseMenuAction,
} from "./bridgeEvents";

describe("bridge event validation", () => {
  it("constructs a typed complete approval event without trusting unknown fields", () => {
    expect(
      parseAgentEvent([
        {
          runId: "run-1",
          seq: 3,
          type: "approval_request",
          callId: "call-1",
          tool: "write_file",
          args: "{}",
          intent: "{\"version\":1}",
          intentDigest: "a".repeat(64),
          expiresAt: "2030-01-01T00:00:00Z",
          ignored: { unsafe: true },
        },
      ]),
    ).toEqual({
      runId: "run-1",
      seq: 3,
      type: "approval_request",
      callId: "call-1",
      tool: "write_file",
      args: "{}",
      intent: "{\"version\":1}",
      intentDigest: "a".repeat(64),
      expiresAt: "2030-01-01T00:00:00Z",
    });
  });

  it.each([
    { runId: "run", seq: 1, type: "plan", plan: "not-an-array" },
    { runId: "run", seq: 1, type: "plan", plan: [{ title: "x", status: "invented" }] },
    { runId: "run", seq: 1.5, type: "done" },
    { runId: "run", seq: 1, type: "approval_request", intentDigest: 42 },
    { runId: "run", seq: 1, type: "approval_request", callId: "call", tool: "write_file", args: "{}" },
    { runId: "run", seq: 1, type: "assistant_text" },
    { runId: "run", seq: 1, type: "tool_result", callId: "call", tool: "read_file" },
    { runId: "run", seq: 1, type: "plan" },
    { runId: "run", seq: 1, type: "unknown" },
  ])("rejects malformed agent payload %#", (payload) => {
    expect(parseAgentEvent(payload)).toBeNull();
  });

  it("validates LLM, filesystem, and menu payloads without string coercion", () => {
    expect(parseLLMEvent({ id: "req", seq: 1, delta: "ok" })).toEqual({ id: "req", seq: 1, delta: "ok" });
    expect(parseLLMEvent({ id: "req", seq: "1", delta: "bad" })).toBeNull();
    expect(parseChangedPath({ path: "src/a.ts" })).toBe("src/a.ts");
    expect(parseChangedPath({ path: 12 })).toBeNull();
    expect(parseErrorMessage([{ message: "watch failed" }])).toBe("watch failed");
    expect(parseErrorMessage({ message: false })).toBeNull();
    expect(parseMenuAction("save")).toBe("save");
    expect(parseMenuAction({ toString: () => "quit" })).toBeNull();
    expect(parseMenuAction("invented_action")).toBeNull();
  });

  it("validates big-file job lifecycle events", () => {
    expect(parseBigFileJobStart([{ id: "job1", title: "CSV filter" }])).toEqual({
      id: "job1",
      title: "CSV filter",
    });
    expect(parseBigFileJobProgress({ id: "job1", records: 1200, note: "reading" })).toEqual({
      id: "job1",
      records: 1200,
      note: "reading",
    });
    expect(parseBigFileJobEnd({ id: "job1" })).toEqual({ id: "job1" });
    expect(parseBigFileJobStart({ id: "job1", title: 4 })).toBeNull();
    expect(parseBigFileJobProgress({ id: "job1", records: -1, note: "bad" })).toBeNull();
    expect(parseBigFileJobProgress({ id: "job1", records: 1, note: null })).toBeNull();
    expect(parseBigFileJobEnd({ id: "" })).toBeNull();
  });
});
