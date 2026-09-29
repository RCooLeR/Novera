/**
 * Small monotonic generation gate for async UI work. Starting a request makes
 * every older lease obsolete; invalidation also prevents an in-flight request
 * from committing after unmount or a resource replacement.
 */
export class LatestRequest {
  private generation = 0;

  begin(): number {
    return ++this.generation;
  }

  isCurrent(generation: number): boolean {
    return generation === this.generation;
  }

  invalidate(): void {
    this.generation++;
  }
}
