import { useEffect, useRef } from "react";
import { FilePlus, FolderPlus, Pencil, Trash2 } from "lucide-react";
import { nextLinearIndex, type LinearNavigationKey } from "../lib/keyboardNavigation";
import { useStore } from "../state/store";

export default function FileContextMenu() {
  const menu = useStore((s) => s.fileMenu);
  const close = useStore((s) => s.closeFileMenu);
  const startNewFile = useStore((s) => s.startNewFile);
  const startNewFolder = useStore((s) => s.startNewFolder);
  const startRename = useStore((s) => s.startRename);
  const requestDelete = useStore((s) => s.requestDelete);
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!menu) return;
    returnFocusRef.current = document.activeElement as HTMLElement | null;
    requestAnimationFrame(() => itemRefs.current[0]?.focus());
    const restoreAndClose = () => {
      close();
      requestAnimationFrame(() => returnFocusRef.current?.focus());
    };
    const onDown = () => restoreAndClose();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        restoreAndClose();
      }
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [menu, close]);

  if (!menu) return null;

  const parent = menu.isDir ? menu.rel : menu.rel.includes("/") ? menu.rel.slice(0, menu.rel.lastIndexOf("/")) : "";
  const x = Math.min(menu.x, window.innerWidth - 190);
  const y = Math.min(menu.y, window.innerHeight - 160);

  const onMenuKeyDown = (event: React.KeyboardEvent) => {
    if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const current = Math.max(0, itemRefs.current.indexOf(document.activeElement as HTMLButtonElement));
    const next = nextLinearIndex(current, itemRefs.current.length, event.key as LinearNavigationKey);
    itemRefs.current[next]?.focus();
  };

  return (
    <div
      className="ctxmenu"
      role="menu"
      aria-label={`File actions for ${menu.name}`}
      style={{ left: x, top: y }}
      onMouseDown={(e) => e.stopPropagation()}
      onKeyDown={onMenuKeyDown}
    >
      <button ref={(el) => { itemRefs.current[0] = el; }} role="menuitem" className="ctxmenu__item" onClick={() => startNewFile(parent)}>
        <FilePlus size={14} /> New File
      </button>
      <button ref={(el) => { itemRefs.current[1] = el; }} role="menuitem" className="ctxmenu__item" onClick={() => startNewFolder(parent)}>
        <FolderPlus size={14} /> New Folder
      </button>
      <div className="ctxmenu__sep" role="separator" />
      <button ref={(el) => { itemRefs.current[2] = el; }} role="menuitem" className="ctxmenu__item" onClick={() => startRename(menu.rel, menu.name)}>
        <Pencil size={14} /> Rename
      </button>
      <button ref={(el) => { itemRefs.current[3] = el; }} role="menuitem" className="ctxmenu__item ctxmenu__item--danger" onClick={() => requestDelete(menu.rel, menu.name)}>
        <Trash2 size={14} /> Delete
      </button>
    </div>
  );
}
