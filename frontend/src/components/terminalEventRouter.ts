export type TerminalRoutedEvent =
  | { kind: "data"; data: string }
  | { kind: "exit" }
  | { kind: "overflow" };

interface PendingSession {
  events: TerminalRoutedEvent[];
  encodedBytes: number;
  overflowed: boolean;
}

// Terminal Start can emit output (or even exit) before its bridge promise
// returns the new session ID. This bounded router is installed before Start,
// queues events by their backend ID, then atomically adopts and replays only
// the returned session. It also rejects unbounded early-event amplification.
export class TerminalEventRouter {
  private activeId = "";
  private readonly pending = new Map<string, PendingSession>();

  constructor(
    private readonly maxPendingSessions = 8,
    private readonly maxPendingEvents = 256,
    private readonly maxPendingEncodedBytes = 1024 * 1024,
  ) {}

  deactivate(): void {
    this.activeId = "";
  }

  activate(id: string): TerminalRoutedEvent[] {
    this.activeId = id;
    const queued = this.pending.get(id);
    this.pending.delete(id);
    return queued?.events ?? [];
  }

  data(id: string, data: string): TerminalRoutedEvent[] {
    if (id === this.activeId && id !== "") return [{ kind: "data", data }];
    this.enqueue(id, { kind: "data", data }, data.length);
    return [];
  }

  exit(id: string): TerminalRoutedEvent[] {
    if (id === this.activeId && id !== "") return [{ kind: "exit" }];
    this.enqueue(id, { kind: "exit" }, 0);
    return [];
  }

  private enqueue(id: string, event: TerminalRoutedEvent, encodedBytes: number): void {
    if (!id) return;
    let session = this.pending.get(id);
    if (!session) {
      if (this.pending.size >= this.maxPendingSessions) {
        const oldest = this.pending.keys().next().value as string | undefined;
        if (oldest) this.pending.delete(oldest);
      }
      session = { events: [], encodedBytes: 0, overflowed: false };
      this.pending.set(id, session);
    }
    if (
      session.events.length >= this.maxPendingEvents ||
      session.encodedBytes + encodedBytes > this.maxPendingEncodedBytes
    ) {
      if (!session.overflowed) {
        session.events.push({ kind: "overflow" });
        session.overflowed = true;
      }
      return;
    }
    session.events.push(event);
    session.encodedBytes += encodedBytes;
  }
}
