export type CancelBigFileSearch = (requestId: string) => void;

/**
 * Owns the one backend search cancellation identity associated with a viewer.
 * A stale completion can never clear or cancel a newer request.
 */
export class BigFileSearchRequestOwner {
  private active = "";

  constructor(private readonly cancelRequest: CancelBigFileSearch) {}

  activate(requestId: string): void {
    if (!/^search[1-9]\d{0,18}$/.test(requestId)) {
      throw new Error("Backend returned an invalid search request identity.");
    }
    if (this.active === requestId) return;
    const previous = this.active;
    this.active = requestId;
    if (previous) this.cancelRequest(previous);
  }

  settle(requestId: string): void {
    if (this.active === requestId) this.active = "";
  }

  cancel(): void {
    const requestId = this.active;
    this.active = "";
    if (requestId) this.cancelRequest(requestId);
  }

  get activeRequestId(): string {
    return this.active;
  }
}
