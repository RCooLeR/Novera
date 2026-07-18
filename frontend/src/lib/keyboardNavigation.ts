export type LinearNavigationKey = "ArrowLeft" | "ArrowRight" | "ArrowUp" | "ArrowDown" | "Home" | "End";

/** Resolve a standard wrapping, one-dimensional keyboard navigation command. */
export function nextLinearIndex(current: number, count: number, key: LinearNavigationKey): number {
  if (count <= 0) return -1;
  const safeCurrent = Math.min(Math.max(current, 0), count - 1);
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  if (key === "ArrowLeft" || key === "ArrowUp") return (safeCurrent - 1 + count) % count;
  return (safeCurrent + 1) % count;
}
