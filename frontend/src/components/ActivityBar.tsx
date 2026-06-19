import { Files, Search, GitBranch, Database, Package, SquareTerminal, Settings } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useStore } from "../state/store";
import type { ViewId } from "../state/store";

const VIEWS: { id: ViewId; icon: LucideIcon; label: string }[] = [
  { id: "explorer", icon: Files, label: "Explorer" },
  { id: "search", icon: Search, label: "Search" },
  { id: "git", icon: GitBranch, label: "Source Control" },
  { id: "db", icon: Database, label: "Database" },
  { id: "artifacts", icon: Package, label: "Artifacts" },
];

export default function ActivityBar() {
  const view = useStore((s) => s.view);
  const setView = useStore((s) => s.setView);
  const sidebarVisible = useStore((s) => s.sidebarVisible);
  const toggleSidebar = useStore((s) => s.toggleSidebar);
  const panelVisible = useStore((s) => s.panelVisible);
  const togglePanel = useStore((s) => s.togglePanel);

  const onView = (id: ViewId) => {
    if (id === view && sidebarVisible) {
      toggleSidebar();
    } else {
      setView(id);
      if (!sidebarVisible) toggleSidebar();
    }
  };

  return (
    <div className="activitybar">
      {VIEWS.map(({ id, icon: Icon, label }) => {
        const active = view === id && sidebarVisible;
        return (
          <button
            key={id}
            className={`activitybar__item ${active ? "active" : ""}`}
            title={label}
            aria-label={label}
            aria-pressed={active}
            onClick={() => onView(id)}
          >
            <Icon size={22} strokeWidth={1.6} aria-hidden focusable={false} />
          </button>
        );
      })}
      <button
        className={`activitybar__item ${panelVisible ? "active" : ""}`}
        title="Terminal (Ctrl+`)"
        aria-label="Terminal"
        aria-pressed={panelVisible}
        onClick={() => togglePanel()}
      >
        <SquareTerminal size={22} strokeWidth={1.6} aria-hidden focusable={false} />
      </button>
      <div className="activitybar__spacer" />
      <button
        className={`activitybar__item ${view === "settings" && sidebarVisible ? "active" : ""}`}
        title="Settings"
        aria-label="Settings"
        aria-pressed={view === "settings" && sidebarVisible}
        onClick={() => onView("settings")}
      >
        <Settings size={22} strokeWidth={1.6} aria-hidden focusable={false} />
      </button>
    </div>
  );
}
