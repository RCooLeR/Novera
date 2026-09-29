// Clamp before calculating spacers: a scroll position from a previous, larger
// result must never leave the smaller result hidden behind an oversized spacer.
export function virtualRange(total: number, scrollTop: number, viewportHeight: number, rowHeight: number, overscan: number, headerHeight = 0) {
  const bodyHeight = Math.max(0, viewportHeight - headerHeight);
  const maxScroll = Math.max(0, total * rowHeight - bodyHeight);
  const top = Math.max(0, Math.min(scrollTop, maxScroll));
  const start = Math.max(0, Math.floor(top / rowHeight) - overscan);
  const end = Math.min(total, Math.ceil((top + bodyHeight) / rowHeight) + overscan);
  return { start, end, top, topPad: start * rowHeight, bottomPad: (total - end) * rowHeight };
}
