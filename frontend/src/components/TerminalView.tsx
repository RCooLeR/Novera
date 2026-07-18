import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Events } from "@wailsio/runtime";
import "@xterm/xterm/css/xterm.css";
import { Term } from "../lib/services";
import { useStore } from "../state/store";
import { TerminalEventRouter } from "./terminalEventRouter";
import type { TerminalRoutedEvent } from "./terminalEventRouter";

interface DataPayload {
  id: string;
  data: string;
}

// Serialize terminal Start/Close calls across React effect generations. This
// ensures a workspace change cannot start the replacement PTY before the old
// effect's pending Start has either been adopted or closed.
let terminalLifecycle: Promise<void> = Promise.resolve();

function enqueueTerminalLifecycle<T>(operation: () => Promise<T>): Promise<T> {
  const result = terminalLifecycle.then(operation, operation);
  terminalLifecycle = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

function payloadOf(e: { data: unknown }): DataPayload | null {
  const d = Array.isArray(e.data) ? e.data[0] : e.data;
  if (
    d &&
    typeof d === "object" &&
    "id" in d &&
    typeof d.id === "string" &&
    "data" in d &&
    typeof d.data === "string"
  ) {
    return { id: d.id, data: d.data };
  }
  return null;
}

function exitIdOf(e: { data: unknown }): string {
  const d = Array.isArray(e.data) ? e.data[0] : e.data;
  if (d && typeof d === "object" && "id" in d && typeof d.id === "string") return d.id;
  return "";
}

function decodeBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

export default function TerminalView() {
  const ref = useRef<HTMLDivElement | null>(null);
  const isOpen = useStore((state) => state.isOpen);
  const workspaceInstanceId = useStore((state) => state.workspaceInstanceId);

  useEffect(() => {
    if (!isOpen) return;
    const el = ref.current;
    if (!el) return;

    const term = new XTerm({
      fontFamily: "Cascadia Code, JetBrains Mono, Consolas, monospace",
      fontSize: 13,
      cursorBlink: true,
      theme: {
        background: "#0d1117",
        foreground: "#c9d1d9",
        cursor: "#4c8dff",
        selectionBackground: "#264f78",
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon());
    term.open(el);

    let id = "";
    let closed = true; // gated until a session id is assigned (or after exit)
    let starting = false;
    let started = false; // first fit+start is deferred until the element has size
    let disposed = false;
    const eventRouter = new TerminalEventRouter();

    const reportError = (operation: string, error: unknown) => {
      if (!disposed) term.writeln(`\r\n\x1b[31m[terminal ${operation} failed: ${String(error)}]\x1b[0m`);
    };

    const applyEvents = (events: TerminalRoutedEvent[]) => {
      for (const event of events) {
        if (event.kind === "data") {
          try {
            term.write(decodeBase64(event.data));
          } catch (error) {
            reportError("output decode", error);
          }
        } else if (event.kind === "exit") {
          closed = true;
          term.writeln("\r\n\x1b[90m[process exited — press Enter to restart]\x1b[0m");
        } else {
          term.writeln("\r\n\x1b[33m[early terminal output exceeded the safety buffer and was truncated]\x1b[0m");
        }
      }
    };

    // Subscribe before Start: the backend read loop begins before the Start
    // promise is delivered, so output and exit must be queued by backend ID
    // until that ID is adopted below.
    const offData = Events.On("term:data", (e: { data: unknown }) => {
      const payload = payloadOf(e);
      if (payload) applyEvents(eventRouter.data(payload.id, payload.data));
    });
    const offExit = Events.On("term:exit", (e: { data: unknown }) => {
      const eventId = exitIdOf(e);
      if (eventId) applyEvents(eventRouter.exit(eventId));
    });

    // Coalesce keystrokes / paste chunks into a single IPC write per microtask
    // instead of one round-trip per character.
    let writeBuf = "";
    let flushScheduled = false;
    const flushWrites = () => {
      flushScheduled = false;
      const data = writeBuf;
      writeBuf = "";
      if (data && !closed && id) void Term.Write(id, data).catch((error) => reportError("write", error));
    };
    const queueWrite = (d: string) => {
      writeBuf += d;
      if (!flushScheduled) {
        flushScheduled = true;
        queueMicrotask(flushWrites);
      }
    };

    const start = async () => {
      if (starting) return; // ignore a second restart while one is in flight
      starting = true;
      id = ""; // drop the stale id so a late write can't hit a dead session
      eventRouter.deactivate();
      try {
        await enqueueTerminalLifecycle(async () => {
          const newId = await Term.Start(term.cols, term.rows);
          if (disposed) {
            await Term.Close(newId);
            return;
          }
          id = newId;
          closed = false;
          applyEvents(eventRouter.activate(newId));
        });
      } catch (err) {
        if (disposed) console.error("terminal lifecycle failed after disposal", err);
        else term.writeln("Failed to start terminal: " + String(err));
      } finally {
        starting = false;
      }
    };

    // Defer the first fit()+start() until the element actually has a size — the
    // panel can mount the terminal hidden (zero-sized), which would otherwise
    // start the PTY with bogus geometry.
    const ensureStarted = () => {
      if (started || el.clientWidth === 0 || el.clientHeight === 0) return;
      started = true;
      fit.fit();
      void start();
    };

    term.onData((d) => {
      if (closed) {
        if (d === "\r") void start(); // restart after the shell exited
        return;
      }
      if (id) queueWrite(d);
    });

    ensureStarted();

    const onResize = () => {
      if (!started) {
        ensureStarted();
        return;
      }
      fit.fit();
      if (id && !closed) void Term.Resize(id, term.cols, term.rows).catch((error) => reportError("resize", error));
    };
    const ro = new ResizeObserver(onResize);
    ro.observe(el);

    return () => {
      disposed = true;
      ro.disconnect();
      offData();
      offExit();
      void enqueueTerminalLifecycle(async () => {
        if (id) await Term.Close(id);
      }).catch((error) => console.error("terminal close failed", error));
      term.dispose();
    };
  }, [isOpen, workspaceInstanceId]);

  if (!isOpen) {
    return <div className="panel__empty" role="status">Open a folder to start a terminal.</div>;
  }
  return <div className="term" ref={ref} key={workspaceInstanceId} />;
}
