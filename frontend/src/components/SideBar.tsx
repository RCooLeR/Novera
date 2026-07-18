import { RefreshCw, ChevronsDownUp, FilePlus, FolderPlus } from "lucide-react";
import { useStore } from "../state/store";
import FileTree from "./FileTree";
import SourceControl from "./SourceControl";
import DatabasePanel from "./DatabasePanel";
import Search from "./Search";
import SettingsView from "./SettingsView";
import ArtifactsView from "./ArtifactsView";
import Splitter from "./Splitter";

export default function SideBar() {
  const view = useStore((s) => s.view);
  const wsName = useStore((s) => s.wsName);
  const refreshTree = useStore((s) => s.refreshTree);
  const loadGitStatus = useStore((s) => s.loadGitStatus);
  const startNewFile = useStore((s) => s.startNewFile);
  const startNewFolder = useStore((s) => s.startNewFolder);
  const resizeSidebar = useStore((s) => s.resizeSidebar);
  const sidebarWidth = useStore((s) => s.sidebarWidth);

  const collapseAll = () => useStore.setState({ expanded: { "": true } });

  let title = "Explorer";
  if (view === "git") title = "Source Control";
  else if (view === "search") title = "Search";
  else if (view === "db") title = "Database";
  else if (view === "artifacts") title = "Artifacts";
  else if (view === "settings") title = "Settings";
  else if (view === "explorer") title = wsName || "Explorer";

  return (
    <div className="sidebar">
      <div className="sidebar__header">
        <span>{title}</span>
        {view === "explorer" && (
          <div className="sidebar__actions">
            <button className="icon-btn" title="New File" onClick={() => startNewFile("")}>
              <FilePlus size={14} />
            </button>
            <button className="icon-btn" title="New Folder" onClick={() => startNewFolder("")}>
              <FolderPlus size={14} />
            </button>
            <button className="icon-btn" title="Refresh" onClick={() => void refreshTree()}>
              <RefreshCw size={14} />
            </button>
            <button className="icon-btn" title="Collapse all" onClick={collapseAll}>
              <ChevronsDownUp size={14} />
            </button>
          </div>
        )}
        {view === "git" && (
          <div className="sidebar__actions">
            <button className="icon-btn" title="Refresh" onClick={() => void loadGitStatus()}>
              <RefreshCw size={14} />
            </button>
          </div>
        )}
      </div>
      <div className="sidebar__body">
        {view === "explorer" && <FileTree />}
        {view === "git" && <SourceControl />}
        {view === "db" && <DatabasePanel />}
        {view === "search" && <Search />}
        {view === "artifacts" && <ArtifactsView />}
        {view === "settings" && <SettingsView />}
      </div>
      <Splitter
        axis="x"
        side="right"
        value={sidebarWidth}
        min={180}
        max={640}
        label="Resize sidebar"
        onResize={resizeSidebar}
      />
    </div>
  );
}
