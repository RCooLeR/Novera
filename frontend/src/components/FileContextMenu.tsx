import { useEffect } from "react";
import { FilePlus, FolderPlus, Pencil, Trash2 } from "lucide-react";
import { useStore } from "../state/store";

export default function FileContextMenu() {
  const menu = useStore((s) => s.fileMenu);
  const close = useStore((s) => s.closeFileMenu);
  const startNewFile = useStore((s) => s.startNewFile);
  const startNewFolder = useStore((s) => s.startNewFolder);
  const startRename = useStore((s) => s.startRename);
  const requestDelete = useStore((s) => s.requestDelete);

  useEffect(() => {
    if (!menu) return;
    const onDown = () => close();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
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

  return (
    <div className="ctxmenu" style={{ left: x, top: y }} onMouseDown={(e) => e.stopPropagation()}>
      <button className="ctxmenu__item" onClick={() => startNewFile(parent)}>
        <FilePlus size={14} /> New File
      </button>
      <button className="ctxmenu__item" onClick={() => startNewFolder(parent)}>
        <FolderPlus size={14} /> New Folder
      </button>
      <div className="ctxmenu__sep" />
      <button className="ctxmenu__item" onClick={() => startRename(menu.rel, menu.name)}>
        <Pencil size={14} /> Rename
      </button>
      <button className="ctxmenu__item ctxmenu__item--danger" onClick={() => requestDelete(menu.rel, menu.name)}>
        <Trash2 size={14} /> Delete
      </button>
    </div>
  );
}
