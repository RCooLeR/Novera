import { lazy, Suspense, useEffect, useState } from "react";
import { Events } from "@wailsio/runtime";
import { isUnsavedResourceTab, useStore } from "./state/store";
import BootSplash from "./components/BootSplash";
import { runMenuAction } from "./lib/menuActions";
import {
  parseAgentEvent,
  parseBigFileJobEnd,
  parseBigFileJobProgress,
  parseBigFileJobStart,
  parseChangedPath,
  parseErrorMessage,
  parseLLMEvent,
  parseMenuAction,
} from "./lib/bridgeEvents";
import TitleBar from "./components/TitleBar";
import AssistantPanel from "./components/AssistantPanel";
import ActivityBar from "./components/ActivityBar";
import SideBar from "./components/SideBar";
import EditorTabs, { editorTabId } from "./components/EditorTabs";
import Breadcrumb from "./components/Breadcrumb";
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
import BigToolsModal from "./components/BigToolsModal";
import AboutModal from "./components/AboutModal";
import { fitPanelLayout } from "./lib/layoutSizing";
import { errMessage, Shell } from "./lib/services";
import { handleNativeCloseRequest } from "./lib/nativeClose";
import { isRecoverableWatchError, watchRecovery } from "./lib/watchRecovery";

// Monaco and its language workers are by far the largest renderer dependency.
// Keep them out of the welcome-screen startup graph and load the editor pane
// only after a workspace actually needs it.
const EditorPane = lazy(() => import("./components/EditorPaneRouter"));

// Preserve dirty-state report ordering even if the bridge executes service
// calls concurrently. A late "dirty" report must not overwrite a newer clean
// state (or vice versa) in the native close gate.
let unsavedResourceReport = Promise.resolve();

export default function App() {
  // One-shot boot splash, shown over the whole app until startup finishes.
  const [booted, setBooted] = useState(false);
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth);
  const appReady = useStore((s) => s.appReady);
  const init = useStore((s) => s.init);
  const isOpen = useStore((s) => s.isOpen);
  const root = useStore((s) => s.root);
  const workspaceInstanceId = useStore((s) => s.workspaceInstanceId);
  const sidebarVisible = useStore((s) => s.sidebarVisible);
  const panelVisible = useStore((s) => s.panelVisible);
  const panelMounted = useStore((s) => s.panelMounted);
  const assistantVisible = useStore((s) => s.assistantVisible);
  const sidebarWidth = useStore((s) => s.sidebarWidth);
  const assistantWidth = useStore((s) => s.assistantWidth);
  const pickAndOpen = useStore((s) => s.pickAndOpen);
  const saveActive = useStore((s) => s.saveActive);
  const toggleSidebar = useStore((s) => s.toggleSidebar);
  const togglePanel = useStore((s) => s.togglePanel);
  const openPalette = useStore((s) => s.openPalette);
  const appendDelta = useStore((s) => s.appendDelta);
  const finishStream = useStore((s) => s.finishStream);
  const failStream = useStore((s) => s.failStream);
  const handleAgentEvent = useStore((s) => s.handleAgentEvent);
  const reloadIfChanged = useStore((s) => s.reloadIfChanged);
  const syncWatches = useStore((s) => s.syncWatches);
  const setStatus = useStore((s) => s.setStatus);
  const applyBigFileJobEvent = useStore((s) => s.applyBigFileJobEvent);
  const activePath = useStore((s) => s.activePath);
  const activeTabIndex = useStore((s) => s.tabs.findIndex((tab) => tab.path === s.activePath));
  const requestCloseTab = useStore((s) => s.requestCloseTab);
  const hasUnsavedResources = useStore((s) => s.tabs.some(isUnsavedResourceTab));

  useEffect(() => {
    void init();
  }, [init]);

  useEffect(() => {
    const updateViewportWidth = () => setViewportWidth(window.innerWidth);
    window.addEventListener("resize", updateViewportWidth);
    return () => window.removeEventListener("resize", updateViewportWidth);
  }, []);

  useEffect(() => {
    unsavedResourceReport = unsavedResourceReport
      .then(() => Shell.SetUnsavedResources(hasUnsavedResources))
      .catch((error: unknown) => {
        setStatus(`Could not update the native close guard: ${errMessage(error)}`, "error");
      });
  }, [hasUnsavedResources, setStatus]);

  useEffect(() => {
    if (!hasUnsavedResources) return;
    const protectUnsavedResources = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", protectUnsavedResources);
    return () => window.removeEventListener("beforeunload", protectUnsavedResources);
  }, [hasUnsavedResources]);

  useEffect(() => {
    // A replacement workspace is a new recovery domain. Do not carry a failed
    // watcher's retry latch across roots.
    watchRecovery.reset();
  }, [workspaceInstanceId]);

  // Route streamed LLM tokens from Go events into the chat store.
  useEffect(() => {
    // Event payloads cross the Go↔JS bridge as `unknown`; validate field types at
    // runtime before use rather than blindly casting (a malformed/empty payload
    // would otherwise feed `undefined` into the store).
    const rejectChatEvent = (message: string) => {
      setStatus(message, "error");
      useStore.getState().cancelChat();
    };
    const offDelta = Events.On("llm:delta", (e: { data: unknown }) => {
      const payload = parseLLMEvent(e.data);
      if (payload && payload.delta !== undefined) appendDelta(payload.id, payload.delta, payload.seq);
      else rejectChatEvent("Stopped the assistant after receiving a malformed stream event.");
    });
    const offDone = Events.On("llm:done", (e: { data: unknown }) => {
      const payload = parseLLMEvent(e.data);
      if (payload) finishStream(payload.id, payload.seq);
      else rejectChatEvent("Stopped the assistant after receiving a malformed completion event.");
    });
    const offErr = Events.On("llm:error", (e: { data: unknown }) => {
      const payload = parseLLMEvent(e.data);
      if (payload && payload.message !== undefined) failStream(payload.id, payload.message || "Assistant error", payload.seq);
      else rejectChatEvent("Stopped the assistant after receiving a malformed error event.");
    });
    const offAgent = Events.On("agent:event", (e: { data: unknown }) => {
      const payload = parseAgentEvent(e.data);
      if (payload) {
        handleAgentEvent(payload);
      } else {
        rejectChatEvent("Stopped the agent after receiving a malformed event.");
      }
    });
    const offFs = Events.On("fs:changed", (e: { data: unknown }) => {
      const path = parseChangedPath(e.data);
      if (path) void reloadIfChanged(path);
      else setStatus("Ignored a malformed filesystem change event.", "error");
    });
    const offFsError = Events.On("fs:watch-error", (e: { data: unknown }) => {
      const message = parseErrorMessage(e.data);
      if (message) {
        setStatus(message, "error");
        // Runtime watcher failures retire the backend generation. One live
        // synchronization asks it to rebuild; the coordinator coalesces an
        // error burst and blocks a failed Watch -> error -> Watch loop.
        if (isRecoverableWatchError(message)) {
          watchRecovery.request(async () => {
            const recovered = await syncWatches();
            if (recovered) setStatus("File watcher recovered and open files are being monitored again.", "success");
            return recovered;
          });
        }
      } else {
        setStatus("The filesystem watcher reported a malformed error event.", "error");
      }
    });
    const offJobs = Events.On("jobs:changed", () => {
      void useStore.getState().loadJobs();
    });
    const offArtifacts = Events.On("artifacts:changed", () => {
      void useStore.getState().loadArtifacts();
    });
    const rejectBigFileJobEvent = () => setStatus("Ignored a malformed big-file job event.", "error");
    const offBigFileJobStart = Events.On("bigfile:job-start", (e: { data: unknown }) => {
      const payload = parseBigFileJobStart(e.data);
      if (payload) applyBigFileJobEvent("start", payload);
      else rejectBigFileJobEvent();
    });
    const offBigFileJobProgress = Events.On("bigfile:job-progress", (e: { data: unknown }) => {
      const payload = parseBigFileJobProgress(e.data);
      if (payload) applyBigFileJobEvent("progress", payload);
      else rejectBigFileJobEvent();
    });
    const offBigFileJobEnd = Events.On("bigfile:job-end", (e: { data: unknown }) => {
      const payload = parseBigFileJobEnd(e.data);
      if (payload) applyBigFileJobEvent("end", payload);
      else rejectBigFileJobEvent();
    });
    const offMenu = Events.On("menu", (e: { data: unknown }) => {
      const action = parseMenuAction(e.data);
      if (action) runMenuAction(action);
      else setStatus("Ignored an unknown application menu action.", "error");
    });
    const offCloseRequested = Events.On("app:close-requested", (e: { data: unknown }) => {
      void handleNativeCloseRequest(e.data, {
        // Read directly at request time. The selected React value and the
        // SetUnsavedResources bridge queue can both lag an immediate edit.
        hasUnsavedResources: () => useStore.getState().tabs.some(isUnsavedResourceTab),
        emitDecision: (decision) => Events.Emit("app:close-decision", decision),
        setStatus,
      });
    });
    const offCloseBlocked = Events.On("app:close-blocked", () => {
      setStatus("Save or explicitly discard all unsaved changes before closing Novera.", "error");
    });
    return () => {
      offDelta();
      offDone();
      offErr();
      offAgent();
      offFs();
      offFsError();
      offJobs();
      offArtifacts();
      offBigFileJobStart();
      offBigFileJobProgress();
      offBigFileJobEnd();
      offMenu();
      offCloseRequested();
      offCloseBlocked();
    };
  }, [appendDelta, applyBigFileJobEvent, finishStream, failStream, handleAgentEvent, reloadIfChanged, setStatus, syncWatches]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!booted) return;
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
      if (mod && key === "o") {
        e.preventDefault();
        void pickAndOpen();
        return;
      }
      if (mod && key === "w" && activePath) {
        e.preventDefault();
        requestCloseTab(activePath);
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
  }, [activePath, booted, pickAndOpen, requestCloseTab, saveActive, toggleSidebar, togglePanel, openPalette]);

  const layout = fitPanelLayout(
    viewportWidth,
    isOpen && sidebarVisible,
    isOpen && assistantVisible,
    sidebarWidth,
    assistantWidth,
  );
  const showSidebar = layout.showSidebar;
  const showAssistant = layout.showAssistant;

  useEffect(() => {
    const fitted: { sidebarWidth?: number; assistantWidth?: number } = {};
    if (showSidebar && layout.sidebarWidth !== sidebarWidth) fitted.sidebarWidth = layout.sidebarWidth;
    if (showAssistant && layout.assistantWidth !== assistantWidth) fitted.assistantWidth = layout.assistantWidth;
    if (Object.keys(fitted).length) useStore.setState(fitted);
  }, [assistantWidth, layout.assistantWidth, layout.sidebarWidth, showAssistant, showSidebar, sidebarWidth]);

  return (
    <div className="app">
      {!booted && <BootSplash ready={appReady} onDone={() => setBooted(true)} />}
      <TitleBar />
      <div
        className="app-body"
        style={{
          gridTemplateColumns: `var(--activitybar-w) ${showSidebar ? layout.sidebarWidth + "px" : "0px"} 1fr ${
            showAssistant ? layout.assistantWidth + "px" : "0px"
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
                <div
                  id="editor-tabpanel"
                  className="editor-host"
                  role="tabpanel"
                  aria-labelledby={activeTabIndex >= 0 ? editorTabId(activeTabIndex) : undefined}
                >
                  <Suspense fallback={<div className="editor-placeholder" role="status">Loading editor…</div>}>
                    <EditorPane />
                  </Suspense>
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
        {showAssistant ? <AssistantPanel key={root} /> : <div style={{ overflow: "hidden" }} />}
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
      <BigToolsModal />
      <AboutModal />
    </div>
  );
}
