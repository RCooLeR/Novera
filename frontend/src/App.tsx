import { useEffect } from "react";
import { Events } from "@wailsio/runtime";
import { useStore } from "./state/store";
import type { ViewId } from "./state/store";
import TitleBar from "./components/TitleBar";
import AssistantPanel from "./components/AssistantPanel";
import ActivityBar from "./components/ActivityBar";
import SideBar from "./components/SideBar";
import EditorTabs from "./components/EditorTabs";
import Breadcrumb from "./components/Breadcrumb";
import EditorPane from "./components/EditorPane";
import StatusBar from "./components/StatusBar";
import Welcome from "./components/Welcome";
import Panel from "./components/Panel";
import FileContextMenu from "./components/FileContextMenu";
import FileOpModal from "./components/FileOpModal";
import ConfirmDeleteModal from "./components/ConfirmDeleteModal";
import CloseTabModal from "./components/CloseTabModal";
import CommandPalette from "./components/CommandPalette";
import ToolsModal from "./components/ToolsModal";
import AuditLogModal from "./components/AuditLogModal";
import CleanDumpModal from "./components/CleanDumpModal";

export default function App() {
  const init = useStore((s) => s.init);
  const isOpen = useStore((s) => s.isOpen);
  const sidebarVisible = useStore((s) => s.sidebarVisible);
  const panelVisible = useStore((s) => s.panelVisible);
  const panelMounted = useStore((s) => s.panelMounted);
  const assistantVisible = useStore((s) => s.assistantVisible);
  const sidebarWidth = useStore((s) => s.sidebarWidth);
  const assistantWidth = useStore((s) => s.assistantWidth);
  const saveActive = useStore((s) => s.saveActive);
  const toggleSidebar = useStore((s) => s.toggleSidebar);
  const togglePanel = useStore((s) => s.togglePanel);
  const openPalette = useStore((s) => s.openPalette);
  const appendDelta = useStore((s) => s.appendDelta);
  const finishStream = useStore((s) => s.finishStream);
  const failStream = useStore((s) => s.failStream);
  const handleAgentEvent = useStore((s) => s.handleAgentEvent);
  const reloadIfChanged = useStore((s) => s.reloadIfChanged);

  useEffect(() => {
    void init();
  }, [init]);

  // Route streamed LLM tokens from Go events into the chat store.
  useEffect(() => {
    // Event payloads cross the Go↔JS bridge as `unknown`; validate field types at
    // runtime before use rather than blindly casting (a malformed/empty payload
    // would otherwise feed `undefined` into the store).
    const pick = (e: { data: unknown }): Record<string, unknown> | null => {
      const d = Array.isArray(e.data) ? e.data[0] : e.data;
      return d && typeof d === "object" ? (d as Record<string, unknown>) : null;
    };
    const str = (v: unknown): string => (typeof v === "string" ? v : "");
    const offDelta = Events.On("llm:delta", (e: { data: unknown }) => {
      const p = pick(e);
      if (p && typeof p.id === "string") appendDelta(p.id, str(p.delta));
    });
    const offDone = Events.On("llm:done", (e: { data: unknown }) => {
      const p = pick(e);
      if (p && typeof p.id === "string") finishStream(p.id);
    });
    const offErr = Events.On("llm:error", (e: { data: unknown }) => {
      const p = pick(e);
      if (p && typeof p.id === "string") failStream(p.id, str(p.message));
    });
    const offAgent = Events.On("agent:event", (e: { data: unknown }) => {
      const p = pick(e);
      if (p && typeof p.runId === "string" && typeof p.type === "string") {
        handleAgentEvent(p as unknown as Parameters<typeof handleAgentEvent>[0]);
      }
    });
    const offFs = Events.On("fs:changed", (e: { data: unknown }) => {
      const p = (Array.isArray(e.data) ? e.data[0] : e.data) as { path?: string };
      if (p?.path) void reloadIfChanged(p.path);
    });
    const offJobs = Events.On("jobs:changed", () => {
      void useStore.getState().loadJobs();
    });
    const offArtifacts = Events.On("artifacts:changed", () => {
      void useStore.getState().loadArtifacts();
    });
    const offMenu = Events.On("menu", (e: { data: unknown }) => {
      const action = String(Array.isArray(e.data) ? e.data[0] : e.data);
      const g = useStore.getState();
      const showView = (v: ViewId) => {
        g.setView(v);
        if (!useStore.getState().sidebarVisible) g.toggleSidebar();
      };
      switch (action) {
        case "open_folder": void g.pickAndOpen(); break;
        case "new_file": g.startNewFile(""); break;
        case "new_folder": g.startNewFolder(""); break;
        case "save": void g.saveActive(); break;
        case "palette": g.openPalette("commands"); break;
        case "quickopen": g.openPalette("files"); break;
        case "view_explorer": showView("explorer"); break;
        case "view_search": showView("search"); break;
        case "view_git": showView("git"); break;
        case "view_db": showView("db"); break;
        case "view_settings": showView("settings"); break;
        case "view_problems": g.showProblems(); break;
        case "tool_csv_schema": void g.runCsvSchema(); break;
        case "tool_csv_to_sql": void g.runCsvToSql(); break;
        case "tool_dump_analyze": void g.runDumpAnalyze(); break;
        case "tool_clean_dump": g.openCleanDump(); break;
        case "tool_save_artifact": void g.saveActiveAsArtifact("file"); break;
        case "toggle_sidebar": g.toggleSidebar(); break;
        case "toggle_panel": g.togglePanel(); break;
        case "toggle_assistant": g.toggleAssistant(); break;
      }
    });
    return () => {
      offDelta();
      offDone();
      offErr();
      offAgent();
      offFs();
      offJobs();
      offArtifacts();
      offMenu();
    };
  }, [appendDelta, finishStream, failStream, handleAgentEvent, reloadIfChanged]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      const key = e.key.toLowerCase();
      // Don't hijack toggle/palette shortcuts while typing in a plain input or
      // textarea (chat box, search, rename modal). The Monaco editor is exempt
      // so quick-open still works from the editor.
      const el = document.activeElement as HTMLElement | null;
      const inPlainField =
        !!el &&
        (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable) &&
        !el.closest(".monaco-editor");
      if (mod && key === "s") {
        // Monaco registers its own Ctrl+S (format-on-save then write). Let it
        // handle the keystroke when the editor is focused so we don't save
        // twice (and bypass format-on-save) from this global handler.
        if (el && el.closest(".monaco-editor")) return;
        e.preventDefault();
        void saveActive();
        return;
      }
      if (inPlainField) return;
      if (mod && e.shiftKey && key === "p") {
        e.preventDefault();
        openPalette("commands");
      } else if (mod && key === "p") {
        e.preventDefault();
        openPalette("files");
      } else if (mod && key === "b") {
        e.preventDefault();
        toggleSidebar();
      } else if (mod && e.key === "`") {
        e.preventDefault();
        togglePanel();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [saveActive, toggleSidebar, togglePanel, openPalette]);

  const showSidebar = isOpen && sidebarVisible;
  const showAssistant = isOpen && assistantVisible;

  return (
    <div className="app">
      <TitleBar />
      <div
        className="app-body"
        style={{
          gridTemplateColumns: `var(--activitybar-w) ${showSidebar ? sidebarWidth + "px" : "0px"} 1fr ${
            showAssistant ? assistantWidth + "px" : "0px"
          }`,
        }}
      >
        <ActivityBar />
        {showSidebar ? <SideBar /> : <div style={{ overflow: "hidden" }} />}
        <div className="app-main">
          {isOpen ? (
            <>
              <div className="editor-area">
                <EditorTabs />
                <Breadcrumb />
                <div className="editor-host">
                  <EditorPane />
                </div>
              </div>
              {panelMounted && <Panel hidden={!panelVisible} />}
            </>
          ) : (
            <div style={{ position: "relative", flex: 1 }}>
              <Welcome />
            </div>
          )}
        </div>
        {showAssistant ? <AssistantPanel /> : <div style={{ overflow: "hidden" }} />}
      </div>
      <StatusBar />
      <FileContextMenu />
      <FileOpModal />
      <ConfirmDeleteModal />
      <CloseTabModal />
      <CommandPalette />
      <ToolsModal />
      <AuditLogModal />
      <CleanDumpModal />
    </div>
  );
}
