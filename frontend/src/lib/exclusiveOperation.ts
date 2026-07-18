export interface OperationLease {
  readonly id: number;
  readonly label: string;
}

/** A non-queuing single-flight gate for destructive or shared-state actions. */
export class ExclusiveOperation {
  private nextId = 0;
  private active: OperationLease | null = null;

  tryAcquire(label: string): OperationLease | null {
    if (this.active) return null;
    const lease = Object.freeze({ id: ++this.nextId, label });
    this.active = lease;
    return lease;
  }

  release(lease: OperationLease): boolean {
    if (this.active !== lease) return false;
    this.active = null;
    return true;
  }

  owns(lease: OperationLease): boolean {
    return this.active === lease;
  }

  activeLabel(): string | null {
    return this.active?.label ?? null;
  }
}
