import { useEffect, useMemo, useRef, useState } from "react";
import { runMenuAction } from "../lib/menuActions";
import type { AppMenuAction } from "../lib/menuActions";
import { useStore } from "../state/store";

type MenuId = "file" | "edit" | "view" | "tools";

type MenuEntry =
  | { type: "separator" }
  | {
      type: "item";
      label: string;
      action: AppMenuAction;
      shortcut?: string;
      disabled?: boolean;
    };

interface MenuDefinition {
  id: MenuId;
  label: string;
  items: MenuEntry[];
}

const separator: MenuEntry = { type: "separator" };

export default function AppMenu() {
  const [open, setOpen] = useState<MenuId | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const isOpen = useStore((s) => s.isOpen);
  const activePath = useStore((s) => s.activePath);
  const activeTab = useStore((s) => s.tabs.find((t) => t.path === s.activePath));
  const toolBusy = useStore((s) => s.toolBusy);

  const canEditFile = !!activeTab && activeTab.kind === "file" && !activeTab.binary && !activeTab.tooLarge;
  const canUseWorkspace = isOpen;
  const canUseFile = !!activePath;
  const isCsv = !!activePath && /\.(csv|tsv)$/i.test(activePath);
  const isDump = !!activePath && /\.(sql|dump)$/i.test(activePath);

  const menus = useMemo<MenuDefinition[]>(
    () => [
      {
        id: "file",
        label: "File",
        items: [
          { type: "item", label: "Open Folder...", action: "open_folder", shortcut: "Ctrl+O" },
          separator,
          { type: "item", label: "New File", action: "new_file", disabled: !canUseWorkspace },
          { type: "item", label: "New Folder", action: "new_folder", disabled: !canUseWorkspace },
          separator,
          { type: "item", label: "Save", action: "save", shortcut: "Ctrl+S", disabled: !canEditFile },
          separator,
          { type: "item", label: "Quit", action: "quit" },
        ],
      },
      {
        id: "edit",
        label: "Edit",
        items: [
          { type: "item", label: "Undo", action: "edit_undo", shortcut: "Ctrl+Z" },
          { type: "item", label: "Redo", action: "edit_redo", shortcut: "Ctrl+Y" },
          separator,
          { type: "item", label: "Cut", action: "edit_cut", shortcut: "Ctrl+X" },
          { type: "item", label: "Copy", action: "edit_copy", shortcut: "Ctrl+C" },
          { type: "item", label: "Paste", action: "edit_paste", shortcut: "Ctrl+V" },
          separator,
          { type: "item", label: "Select All", action: "edit_select_all", shortcut: "Ctrl+A" },
          { type: "item", label: "Format Document", action: "format_document", disabled: !canEditFile },
        ],
      },
      {
        id: "view",
        label: "View",
        items: [
          { type: "item", label: "Command Palette", action: "palette", shortcut: "Ctrl+Shift+P" },
          { type: "item", label: "Go to File...", action: "quickopen", shortcut: "Ctrl+P", disabled: !canUseWorkspace },
          separator,
          { type: "item", label: "Explorer", action: "view_explorer", disabled: !canUseWorkspace },
          { type: "item", label: "Search", action: "view_search", disabled: !canUseWorkspace },
          { type: "item", label: "Source Control", action: "view_git", disabled: !canUseWorkspace },
          { type: "item", label: "Database", action: "view_db", disabled: !canUseWorkspace },
          { type: "item", label: "Artifacts", action: "view_artifacts", disabled: !canUseWorkspace },
          { type: "item", label: "Problems", action: "view_problems", disabled: !canUseWorkspace },
          { type: "item", label: "Settings", action: "view_settings", disabled: !canUseWorkspace },
          separator,
          { type: "item", label: "Toggle Sidebar", action: "toggle_sidebar", shortcut: "Ctrl+B", disabled: !canUseWorkspace },
          { type: "item", label: "Toggle Terminal", action: "toggle_panel", shortcut: "Ctrl+`", disabled: !canUseWorkspace },
          { type: "item", label: "Toggle Assistant", action: "toggle_assistant", disabled: !canUseWorkspace },
        ],
      },
      {
        id: "tools",
        label: "Tools",
        items: [
          { type: "item", label: "Infer CSV Schema", action: "tool_csv_schema", disabled: toolBusy || !isCsv },
          { type: "item", label: "CSV to SQL...", action: "tool_csv_to_sql", disabled: toolBusy || !isCsv },
          separator,
          { type: "item", label: "Analyze SQL Dump", action: "tool_dump_analyze", disabled: toolBusy || !isDump },
          { type: "item", label: "Clean SQL Dump...", action: "tool_clean_dump", disabled: toolBusy || !isDump },
          separator,
          { type: "item", label: "Save File as Artifact", action: "tool_save_artifact", disabled: !canUseFile },
        ],
      },
    ],
    [canEditFile, canUseFile, canUseWorkspace, isCsv, isDump, toolBusy],
  );

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (target instanceof Node && rootRef.current?.contains(target)) return;
      setOpen(null);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(null);
    };
    window.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  const choose = (item: Extract<MenuEntry, { type: "item" }>) => {
    if (item.disabled) return;
    setOpen(null);
    runMenuAction(item.action);
  };

  return (
    <nav className="appmenu no-drag" aria-label="Application menu" ref={rootRef}>
      {menus.map((menu) => (
        <div className="appmenu__root" key={menu.id}>
          <button
            className={`appmenu__trigger ${open === menu.id ? "active" : ""}`}
            aria-haspopup="menu"
            aria-expanded={open === menu.id}
            onClick={() => setOpen((current) => (current === menu.id ? null : menu.id))}
            onMouseEnter={() => {
              if (open) setOpen(menu.id);
            }}
          >
            {menu.label}
          </button>
          {open === menu.id && (
            <div className="appmenu__dropdown" role="menu">
              {menu.items.map((item, index) =>
                item.type === "separator" ? (
                  <div className="appmenu__sep" role="separator" key={`${menu.id}-sep-${index}`} />
                ) : (
                  <button
                    className="appmenu__item"
                    disabled={item.disabled}
                    key={`${menu.id}-${item.action}-${index}`}
                    role="menuitem"
                    onClick={() => choose(item)}
                  >
                    <span>{item.label}</span>
                    {item.shortcut && <span className="appmenu__shortcut">{item.shortcut}</span>}
                  </button>
                ),
              )}
            </div>
          )}
        </div>
      ))}
    </nav>
  );
}
