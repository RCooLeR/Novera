import { useCallback, useEffect, useRef } from "react";

// A thin drag handle. axis "x" resizes width (col-resize), "y" resizes height
// (row-resize). onResize receives the per-move pixel delta; the store applies it
// with a functional update, so there's no stale-closure accumulation bug.
export default function Splitter({
  axis,
  side,
  onResize,
}: {
  axis: "x" | "y";
  side: "left" | "right" | "top";
  onResize: (delta: number) => void;
}) {
  // Holds the teardown for an in-flight drag so it can also run on unmount —
  // otherwise unmounting mid-drag would leak the window listeners.
  const endDrag = useRef<(() => void) | null>(null);
  useEffect(() => () => endDrag.current?.(), []);

  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault();
      const target = e.currentTarget as HTMLElement;
      const pointerId = e.pointerId;
      // Capture the pointer so we keep receiving moves even if it leaves the
      // (thin) handle or the webview loses focus mid-drag.
      try {
        target.setPointerCapture(pointerId);
      } catch {
        /* not all environments support capture; window listeners still work */
      }
      let last = axis === "x" ? e.clientX : e.clientY;
      const move = (ev: PointerEvent) => {
        const cur = axis === "x" ? ev.clientX : ev.clientY;
        onResize(cur - last);
        last = cur;
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        window.removeEventListener("pointercancel", up); // treat cancel like up
        try {
          target.releasePointerCapture(pointerId);
        } catch {
          /* ignore */
        }
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
        endDrag.current = null;
      };
      endDrag.current = up;
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
      window.addEventListener("pointercancel", up);
      document.body.style.cursor = axis === "x" ? "col-resize" : "row-resize";
      document.body.style.userSelect = "none";
    },
    [axis, onResize],
  );

  return <div className={`splitter splitter--${side}`} onPointerDown={onPointerDown} />;
}
