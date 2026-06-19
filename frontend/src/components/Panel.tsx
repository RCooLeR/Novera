import { ListChecks, SquareTerminal, TriangleAlert, X } from "lucide-react";
import { useStore } from "../state/store";
import TerminalView from "./TerminalView";
import ProblemsView from "./ProblemsView";
import JobsView from "./JobsView";
import Splitter from "./Splitter";

export default function Panel({ hidden = false }: { hidden?: boolean }) {
  const togglePanel = useStore((s) => s.togglePanel);
  const panelTab = useStore((s) => s.panelTab);
  const setPanelTab = useStore((s) => s.setPanelTab);
  const diagnostics = useStore((s) => s.diagnostics);
  const panelHeight = useStore((s) => s.panelHeight);
  const resizePanel = useStore((s) => s.resizePanel);
  const problemCount = diagnostics?.items.length ?? 0;
  const runningJobs = useStore((s) => s.jobs.filter((j) => j.status === "running").length);

  return (
    <div className="panel" style={{ height: panelHeight, ...(hidden ? { display: "none" } : {}) }}>
      <Splitter axis="y" side="top" onResize={resizePanel} />
      <div className="panel__header">
        <button
          className={`panel__tab ${panelTab === "terminal" ? "active" : ""}`}
          onClick={() => setPanelTab("terminal")}
        >
          <SquareTerminal size={14} /> Terminal
        </button>
        <button
          className={`panel__tab ${panelTab === "problems" ? "active" : ""}`}
          onClick={() => setPanelTab("problems")}
        >
          <TriangleAlert size={14} /> Problems
          {problemCount > 0 && <span className="panel__badge">{problemCount}</span>}
        </button>
        <button
          className={`panel__tab ${panelTab === "jobs" ? "active" : ""}`}
          onClick={() => setPanelTab("jobs")}
        >
          <ListChecks size={14} /> Jobs
          {runningJobs > 0 && <span className="panel__badge">{runningJobs}</span>}
        </button>
        <span className="panel__spacer" />
        <button className="icon-btn" title="Close panel (Ctrl+`)" aria-label="Close panel" onClick={togglePanel}>
          <X size={14} aria-hidden focusable={false} />
        </button>
      </div>
      <div className="panel__body">
        {/* Terminal stays mounted (keeps the shell alive) and is hidden when not active. */}
        <div style={{ display: panelTab === "terminal" ? "block" : "none", height: "100%" }}>
          <TerminalView />
        </div>
        {panelTab === "problems" && <ProblemsView />}
        {panelTab === "jobs" && <JobsView />}
      </div>
    </div>
  );
}
