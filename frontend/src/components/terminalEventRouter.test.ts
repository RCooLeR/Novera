import { describe, expect, it } from "vitest";
import { TerminalEventRouter } from "./terminalEventRouter";

describe("TerminalEventRouter", () => {
  it("replays synchronous output and exit after Start returns the session ID", () => {
    const router = new TerminalEventRouter();
    expect(router.data("term-1", "prompt")).toEqual([]);
    expect(router.exit("term-1")).toEqual([]);
    expect(router.activate("term-1")).toEqual([{ kind: "data", data: "prompt" }, { kind: "exit" }]);
  });

  it("does not deliver another session's queued output", () => {
    const router = new TerminalEventRouter();
    router.data("old", "old output");
    expect(router.activate("new")).toEqual([]);
    expect(router.data("new", "new output")).toEqual([{ kind: "data", data: "new output" }]);
  });

  it("bounds pre-adoption output and reports overflow", () => {
    const router = new TerminalEventRouter(1, 2, 4);
    router.data("term-1", "12");
    router.data("term-1", "34");
    router.data("term-1", "56");
    expect(router.activate("term-1")).toEqual([
      { kind: "data", data: "12" },
      { kind: "data", data: "34" },
      { kind: "overflow" },
    ]);
  });
});
