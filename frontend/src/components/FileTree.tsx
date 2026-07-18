import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronRight, File, Folder, FolderOpen, Loader2 } from "lucide-react";
import { useStore } from "../state/store";
import type { Entry } from "../lib/services";

// The tree is flattened to the list of currently-visible (expanded) nodes, then
// only the rows in/near the viewport are rendered — so a directory with
// thousands of entries stays responsive. Rows are a fixed 22px (matches CSS),
// so the window math needs no measurement. Full keyboard navigation is wired on
// the flattened list. The scroll container is the parent (.sidebar__body), so
// we observe its scroll rather than forcing a second scroller.
const ROW_H = 22;
const OVERSCAN = 12;

type FlatNode = { entry: Entry; depth: number };

function flatten(
  childrenByPath: Record<string, Entry[]>,
  expanded: Record<string, boolean>,
  root: Entry[],
): FlatNode[] {
  const out: FlatNode[] = [];
  const walk = (entries: Entry[], depth: number) => {
    for (const e of entries) {
      out.push({ entry: e, depth });
      if (e.isDir && expanded[e.path]) {
        const kids = childrenByPath[e.path];
        if (kids) walk(kids, depth + 1);
      }
    }
  };
  walk(root, 0);
  return out;
}

const parentOf = (rel: string) => {
  const i = rel.lastIndexOf("/");
  return i > 0 ? rel.slice(0, i) : "";
};

export default function FileTree() {
  const childrenByPath = useStore((s) => s.childrenByPath);
  const expanded = useStore((s) => s.expanded);
  const loadingPath = useStore((s) => s.loadingPath);
  const selectedPath = useStore((s) => s.selectedPath);
  const toggleDir = useStore((s) => s.toggleDir);
  const openFile = useStore((s) => s.openFile);
  const openFileMenu = useStore((s) => s.openFileMenu);

  const root = childrenByPath[""];
  const rootLoading = loadingPath[""] ?? false;

  const flat = useMemo(() => flatten(childrenByPath, expanded, root ?? []), [childrenByPath, expanded, root]);
  const total = flat.length;
  const selIndex = useMemo(() => flat.findIndex((n) => n.entry.path === selectedPath), [flat, selectedPath]);

  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportH, setViewportH] = useState(600);

  // Track the parent scroll container (the sidebar body) rather than adding a
  // nested scroller.
  useEffect(() => {
    const scroller = wrapRef.current?.parentElement;
    if (!scroller) return;
    const onScroll = () => setScrollTop(scroller.scrollTop);
    const measure = () => setViewportH(scroller.clientHeight);
    scroller.addEventListener("scroll", onScroll, { passive: true });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(scroller);
    return () => {
      scroller.removeEventListener("scroll", onScroll);
      ro.disconnect();
    };
  }, []);

  // Keep the keyboard-selected row in view.
  useEffect(() => {
    const scroller = wrapRef.current?.parentElement;
    if (!scroller || selIndex < 0) return;
    const top = selIndex * ROW_H;
    const bottom = top + ROW_H;
    if (top < scroller.scrollTop) scroller.scrollTop = top;
    else if (bottom > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = bottom - scroller.clientHeight;
  }, [selIndex]);

  if (rootLoading && !root) return <div className="tree__empty">Loading…</div>;
  if (!root || root.length === 0) return <div className="tree__empty">This folder is empty</div>;

  const start = Math.max(0, Math.floor(scrollTop / ROW_H) - OVERSCAN);
  const end = Math.min(total, Math.ceil((scrollTop + viewportH) / ROW_H) + OVERSCAN);
  const visible = flat.slice(start, end);

  const select = (i: number) => {
    if (i >= 0 && i < total) useStore.setState({ selectedPath: flat[i].entry.path });
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (total === 0) return;
    const cur = selIndex < 0 ? 0 : selIndex;
    const node = flat[cur];
    if ((e.shiftKey && e.key === "F10") || e.key === "ContextMenu") {
      e.preventDefault();
      const row = document.getElementById(`treeitem-${cur}`);
      const rect = row?.getBoundingClientRect();
      if (node) {
        openFileMenu({
          x: rect?.left ?? 12,
          y: rect?.bottom ?? 12,
          rel: node.entry.path,
          isDir: node.entry.isDir,
          name: node.entry.name,
        });
      }
      return;
    }
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        select(Math.min(total - 1, cur + 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        select(Math.max(0, cur - 1));
        break;
      case "Home":
        e.preventDefault();
        select(0);
        break;
      case "End":
        e.preventDefault();
        select(total - 1);
        break;
      case "ArrowRight":
        e.preventDefault();
        if (node?.entry.isDir) {
          if (!expanded[node.entry.path]) void toggleDir(node.entry.path);
          else select(cur + 1);
        }
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (node?.entry.isDir && expanded[node.entry.path]) {
          void toggleDir(node.entry.path);
        } else if (node) {
          const pi = flat.findIndex((x) => x.entry.path === parentOf(node.entry.path));
          if (pi >= 0) select(pi);
        }
        break;
      case "Enter":
        e.preventDefault();
        if (node?.entry.isDir) void toggleDir(node.entry.path);
        else if (node) void openFile(node.entry.path, node.entry.name);
        break;
    }
  };

  return (
    <div
      className="tree"
      ref={wrapRef}
      tabIndex={0}
      role="tree"
      aria-label="Workspace files"
      aria-activedescendant={selIndex >= 0 ? `treeitem-${selIndex}` : undefined}
      onKeyDown={onKeyDown}
      style={{ height: total * ROW_H, position: "relative" }}
    >
      <div style={{ position: "absolute", top: start * ROW_H, left: 0, right: 0 }}>
        {visible.map((n, visibleIndex) => {
          const { entry, depth } = n;
          const absoluteIndex = start + visibleIndex;
          const isExpanded = expanded[entry.path] ?? false;
          const loading = loadingPath[entry.path] ?? false;
          const selected = selectedPath === entry.path;
          return (
            <div
              id={`treeitem-${absoluteIndex}`}
              key={entry.path}
              className={`tree__row ${selected ? "selected" : ""}`}
              style={{ paddingLeft: 8 + depth * 14 }}
              role="treeitem"
              aria-selected={selected}
              aria-expanded={entry.isDir ? isExpanded : undefined}
              onClick={() => {
                wrapRef.current?.focus();
                useStore.setState({ selectedPath: entry.path });
                if (entry.isDir) void toggleDir(entry.path);
                else void openFile(entry.path, entry.name);
              }}
              onContextMenu={(e) => {
                e.preventDefault();
                wrapRef.current?.focus();
                useStore.setState({ selectedPath: entry.path });
                openFileMenu({ x: e.clientX, y: e.clientY, rel: entry.path, isDir: entry.isDir, name: entry.name });
              }}
            >
              <span className="tree__twisty">
                {entry.isDir ? isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} /> : null}
              </span>
              <span className="tree__icon">
                {loading ? (
                  <Loader2 size={13} className="spin" />
                ) : entry.isDir ? (
                  isExpanded ? <FolderOpen size={15} color="var(--accent)" /> : <Folder size={15} color="var(--accent)" />
                ) : (
                  <File size={14} />
                )}
              </span>
              <span className="tree__label">{entry.name}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
