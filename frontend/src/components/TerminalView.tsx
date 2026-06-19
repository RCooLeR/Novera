import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Events } from "@wailsio/runtime";
import "@xterm/xterm/css/xterm.css";
import { Term } from "../lib/services";

interface DataPayload {
  id: string;
  data: string;
}

function payloadOf(e: { data: unknown }): DataPayload | null {
  const d = Array.isArray(e.data) ? e.data[0] : e.data;
  if (d && typeof d === "object" && "id" in d) return d as DataPayload;
  return null;
}

function decodeBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

export default function TerminalView() {
  const ref = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
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
    let offData: (() => void) | undefined;
    let offExit: (() => void) | undefined;

    // Coalesce keystrokes / paste chunks into a single IPC write per microtask
    // instead of one round-trip per character.
    let writeBuf = "";
    let flushScheduled = false;
    const flushWrites = () => {
      flushScheduled = false;
      const data = writeBuf;
      writeBuf = "";
      if (data && !closed && id) void Term.Write(id, data);
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
      try {
        const newId = await Term.Start(term.cols, term.rows);
        if (disposed) {
          void Term.Close(newId);
          return;
        }
        id = newId;
        closed = false; // only now is it safe to accept input
        offData?.();
        offExit?.();
        offData = Events.On("term:data", (e: { data: unknown }) => {
          const p = payloadOf(e);
          if (p && p.id === id) term.write(decodeBase64(p.data));
        });
        offExit = Events.On("term:exit", (e: { data: unknown }) => {
          const p = payloadOf(e);
          if (p && p.id === id) {
            closed = true;
            term.writeln("\r\n\x1b[90m[process exited — press Enter to restart]\x1b[0m");
          }
        });
      } catch (err) {
        term.writeln("Failed to start terminal: " + String(err));
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
      if (id && !closed) void Term.Resize(id, term.cols, term.rows);
    };
    const ro = new ResizeObserver(onResize);
    ro.observe(el);

    return () => {
      disposed = true;
      ro.disconnect();
      offData?.();
      offExit?.();
      if (id) void Term.Close(id);
      term.dispose();
    };
  }, []);

  return <div className="term" ref={ref} />;
}
