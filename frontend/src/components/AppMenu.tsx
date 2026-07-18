import { useEffect, useMemo, useRef, useState } from "react";
import { runMenuAction } from "../lib/menuActions";
import type { AppMenuAction } from "../lib/menuActions";
import { resourceCapabilities, resourceCommandEligibility } from "../lib/resourceCapabilities";
import { useStore } from "../state/store";

type MenuId = "file" | "edit" | "view" | "tools" | "help";

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
  const triggerRefs = useRef<Partial<Record<MenuId, HTMLButtonElement | null>>>({});
  const itemRefs = useRef<Partial<Record<MenuId, Array<HTMLButtonElement | null>>>>({});
  const isOpen = useStore((s) => s.isOpen);
  const activeTab = useStore((s) => s.tabs.find((t) => t.path === s.activePath));
  const toolBusy = useStore((s) => s.toolBusy);

  const activeResource = useMemo(() => resourceCapabilities(activeTab), [activeTab]);
  const commandEligibility = useMemo(
    () => resourceCommandEligibility(activeResource, toolBusy),
    [activeResource, toolBusy],
  );
  const canUseWorkspace = isOpen;

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
          { type: "item", label: "Save", action: "save", shortcut: "Ctrl+S", disabled: !commandEligibility.save },
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
          {
            type: "item",
            label: "Format Document",
            action: "format_document",
            disabled: !commandEligibility.formatDocument,
          },
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
          {
            type: "item",
            label: "Infer CSV Schema",
            action: "tool_csv_schema",
            disabled: !commandEligibility.inferCsvSchema,
          },
          {
            type: "item",
            label: "CSV to SQL...",
            action: "tool_csv_to_sql",
            disabled: !commandEligibility.csvToSql,
          },
          separator,
          {
            type: "item",
            label: "Analyze SQL Dump",
            action: "tool_dump_analyze",
            disabled: !commandEligibility.analyzeDump,
          },
          {
            type: "item",
            label: "Clean SQL Dump...",
            action: "tool_clean_dump",
            disabled: !commandEligibility.cleanDump,
          },
          separator,
          {
            type: "item",
            label: "Data Tools (big-file CSV/SQL)...",
            action: "tool_data_tools",
            disabled: !commandEligibility.dataTools,
          },
          {
            type: "item",
            label: "Save File as Artifact",
            action: "tool_save_artifact",
            disabled: !commandEligibility.saveArtifact,
          },
        ],
      },
      {
        id: "help",
        label: "Help",
        items: [
          { type: "item", label: "About Novera", action: "help_about" },
          separator,
          { type: "item", label: "Documentation", action: "help_docs" },
          { type: "item", label: "Report an Issue", action: "help_issues" },
        ],
      },
    ],
    [canUseWorkspace, commandEligibility],
  );

  useEffect(() => {
    if (!open) return;
    const firstEnabled = itemRefs.current[open]?.find((item) => item && !item.disabled);
    firstEnabled?.focus();
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (target instanceof Node && rootRef.current?.contains(target)) return;
      setOpen(null);
    };
    window.addEventListener("pointerdown", onPointerDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
    };
  }, [open]);

  const closeAndRestoreFocus = (menuId: MenuId) => {
    setOpen(null);
    requestAnimationFrame(() => triggerRefs.current[menuId]?.focus());
  };

  const choose = (menuId: MenuId, item: Extract<MenuEntry, { type: "item" }>) => {
    if (item.disabled) return;
    closeAndRestoreFocus(menuId);
    runMenuAction(item.action);
  };

  const focusMenuItem = (menuId: MenuId, direction: "first" | "last" | "next" | "previous") => {
    const items = (itemRefs.current[menuId] ?? []).filter((item): item is HTMLButtonElement => !!item && !item.disabled);
    if (!items.length) return;
    if (direction === "first" || direction === "last") {
      items[direction === "first" ? 0 : items.length - 1].focus();
      return;
    }
    const current = Math.max(0, items.indexOf(document.activeElement as HTMLButtonElement));
    const delta = direction === "next" ? 1 : -1;
    items[(current + delta + items.length) % items.length].focus();
  };

  const switchMenu = (menuId: MenuId, delta: number) => {
    const index = menus.findIndex((menu) => menu.id === menuId);
    setOpen(menus[(index + delta + menus.length) % menus.length].id);
  };

  return (
    <nav className="appmenu no-drag" aria-label="Application menu" ref={rootRef}>
      {menus.map((menu) => (
        <div className="appmenu__root" key={menu.id}>
          <button
            ref={(element) => {
              triggerRefs.current[menu.id] = element;
            }}
            className={`appmenu__trigger ${open === menu.id ? "active" : ""}`}
            aria-haspopup="menu"
            aria-expanded={open === menu.id}
            onClick={() => setOpen((current) => (current === menu.id ? null : menu.id))}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown" || event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                setOpen(menu.id);
              } else if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
                event.preventDefault();
                const index = menus.findIndex((candidate) => candidate.id === menu.id);
                triggerRefs.current[menus[(index + (event.key === "ArrowRight" ? 1 : -1) + menus.length) % menus.length].id]?.focus();
              }
            }}
            onMouseEnter={() => {
              if (open) setOpen(menu.id);
            }}
          >
            {menu.label}
          </button>
          {open === menu.id && (
            <div
              className="appmenu__dropdown"
              role="menu"
              aria-label={menu.label}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  closeAndRestoreFocus(menu.id);
                } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  focusMenuItem(menu.id, event.key === "ArrowDown" ? "next" : "previous");
                } else if (event.key === "Home" || event.key === "End") {
                  event.preventDefault();
                  focusMenuItem(menu.id, event.key === "Home" ? "first" : "last");
                } else if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
                  event.preventDefault();
                  switchMenu(menu.id, event.key === "ArrowRight" ? 1 : -1);
                } else if (event.key.length === 1 && /\S/.test(event.key)) {
                  const items = (itemRefs.current[menu.id] ?? []).filter(
                    (item): item is HTMLButtonElement => !!item && !item.disabled,
                  );
                  const match = items.find((item) => item.textContent?.trim().toLowerCase().startsWith(event.key.toLowerCase()));
                  if (match) {
                    event.preventDefault();
                    match.focus();
                  }
                }
              }}
            >
              {menu.items.map((item, index) =>
                item.type === "separator" ? (
                  <div className="appmenu__sep" role="separator" key={`${menu.id}-sep-${index}`} />
                ) : (
                  <button
                    ref={(element) => {
                      const refs = itemRefs.current[menu.id] ?? [];
                      refs[index] = element;
                      itemRefs.current[menu.id] = refs;
                    }}
                    className="appmenu__item"
                    disabled={item.disabled}
                    key={`${menu.id}-${item.action}-${index}`}
                    role="menuitem"
                    onClick={() => choose(menu.id, item)}
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
