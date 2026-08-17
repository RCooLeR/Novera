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
    const payload = {
      id: "job1",
      sequence: 1,
      title: "CSV filter",
      kind: "transform",
      fileId: "f1",
      completed: 1200,
      total: 2000,
      records: 1200,
      note: "reading",
    };
    expect(parseBigFileJobStart([payload])).toEqual(payload);
    expect(parseBigFileJobProgress(payload)).toEqual(payload);
    expect(parseBigFileJobEnd({ ...payload, status: "completed" })).toEqual({ ...payload, status: "completed" });
    expect(parseBigFileJobStart({ ...payload, sequence: 0 })).toBeNull();
    expect(parseBigFileJobStart({ ...payload, title: 4 })).toBeNull();
    expect(parseBigFileJobProgress({ ...payload, records: -1 })).toBeNull();
    expect(parseBigFileJobProgress({ ...payload, note: null })).toBeNull();
    expect(parseBigFileJobEnd({ ...payload, status: "unknown" })).toBeNull();
  });
});
