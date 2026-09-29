/**
 * Synchronous single-flight ownership for a database query view. React state is
 * still used for presentation, but this owner closes the interval before that
 * state is committed and prevents an obsolete request from publishing results.
 */
export class DbQueryRequestOwner {
  private generation = 0;
  private activeGeneration: number | null = null;

  tryBegin(): number | null {
    if (this.activeGeneration !== null) return null;
    const generation = ++this.generation;
    this.activeGeneration = generation;
    return generation;
  }

  isCurrent(generation: number): boolean {
    return this.activeGeneration === generation;
  }

  finish(generation: number): boolean {
    if (!this.isCurrent(generation)) return false;
    this.activeGeneration = null;
    return true;
  }

  invalidate(): void {
    this.generation++;
    this.activeGeneration = null;
  }
}
