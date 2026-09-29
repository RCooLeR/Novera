// One backend query at a time per table view. Fast scrolling retains only the
// latest requested offset, including while the initial page is still loading.
// A new filter/file owns a new loader; disposal fences all old completions.
export class LatestWindowLoader<T> {
  private disposed = false;
  private running = false;
  private pending: number | null = null;

  constructor(private readonly handlers: {
    query: (offset: number) => Promise<T>;
    loaded: (value: T, offset: number) => void;
    failed: (error: unknown) => void;
    loading: (value: boolean) => void;
  }) {}

  request(offset: number): void {
    if (this.disposed) return;
    if (this.running) {
      this.pending = offset;
      return;
    }
    void this.load(offset);
  }

  dispose(): void {
    this.disposed = true;
    this.pending = null;
  }

  private async load(offset: number): Promise<void> {
    this.running = true;
    this.handlers.loading(true);
    try {
      const value = await this.handlers.query(offset);
      if (!this.disposed) this.handlers.loaded(value, offset);
    } catch (error) {
      if (!this.disposed) this.handlers.failed(error);
    } finally {
      this.running = false;
      if (!this.disposed) {
        const next = this.pending;
        this.pending = null;
        if (next !== null && next !== offset) this.request(next);
        else this.handlers.loading(false);
      }
    }
  }
}
