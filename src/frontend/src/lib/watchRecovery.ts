export type WatchSync = () => Promise<boolean>;

type RecoveryState = "idle" | "recovering" | "blocked";

export function isRecoverableWatchError(message: string): boolean {
  return message.includes("file watcher runtime failure:") || message.includes("file watcher unavailable:");
}

/**
 * Turns a watcher runtime-error event into one recovery attempt. A failed
 * attempt stays blocked because Watch may report that same failure through the
 * event channel before (or after) its promise rejects; immediately retrying
 * would create an event -> Watch -> event loop. Any later successful ordinary
 * watch synchronization, or a workspace replacement, re-arms recovery.
 */
export class WatchRecoveryCoordinator {
  private state: RecoveryState = "idle";

  request(sync: WatchSync): boolean {
    if (this.state !== "idle") return false;
    this.state = "recovering";
    void Promise.resolve()
      .then(sync)
      .then(
        (succeeded) => {
          this.state = succeeded ? "idle" : "blocked";
        },
        () => {
          this.state = "blocked";
        },
      );
    return true;
  }

  observeSyncSuccess(): void {
    this.state = "idle";
  }

  reset(): void {
    this.state = "idle";
  }

  recoveryState(): RecoveryState {
    return this.state;
  }
}

export const watchRecovery = new WatchRecoveryCoordinator();
