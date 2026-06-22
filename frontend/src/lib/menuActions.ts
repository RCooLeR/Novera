import { Application } from "@wailsio/runtime";
import { useStore } from "../state/store";
import type { ViewId } from "../state/store";
import { Shell } from "./services";
import { formatActiveDocument, runActiveEditorAction } from "./editorBridge";

const REPO_URL = "https://github.com/RCooLeR/Novera";

export type AppMenuAction =
  | "open_folder"
  | "new_file"
  | "new_folder"
  | "save"
  | "quit"
  | "edit_undo"
  | "edit_redo"
  | "edit_cut"
  | "edit_copy"
  | "edit_paste"
  | "edit_select_all"
  | "format_document"
  | "palette"
  | "quickopen"
  | "view_explorer"
  | "view_search"
  | "view_git"
  | "view_db"
  | "view_artifacts"
  | "view_settings"
  | "view_problems"
  | "toggle_sidebar"
  | "toggle_panel"
  | "toggle_assistant"
  | "tool_csv_schema"
  | "tool_csv_to_sql"
  | "tool_dump_analyze"
  | "tool_clean_dump"
  | "tool_data_tools"
  | "tool_save_artifact"
  | "help_about"
  | "help_docs"
  | "help_issues";

const editorActionForCommand: Record<string, string> = {
  undo: "undo",
  redo: "redo",
  cut: "editor.action.clipboardCutAction",
  copy: "editor.action.clipboardCopyAction",
  paste: "editor.action.clipboardPasteAction",
  selectAll: "editor.action.selectAll",
};

function browserEdit(command: string) {
  try {
    document.execCommand(command);
  } catch {
    /* Some commands, especially paste, can be blocked by the host browser. */
  }
}

function edit(command: string) {
  const editorAction = editorActionForCommand[command];
  if (!editorAction) {
    browserEdit(command);
    return;
  }
  void runActiveEditorAction(editorAction).then((handled) => {
    if (!handled) browserEdit(command);
  });
}

export function runMenuAction(action: AppMenuAction) {
  const g = useStore.getState();
  const showView = (v: ViewId) => {
    g.setView(v);
    if (!useStore.getState().sidebarVisible) g.toggleSidebar();
  };

  switch (action) {
    case "open_folder":
      void g.pickAndOpen();
      break;
    case "new_file":
      g.startNewFile("");
      break;
    case "new_folder":
      g.startNewFolder("");
      break;
    case "save":
      void g.saveActive();
      break;
    case "quit":
      void Application.Quit();
      break;
    case "edit_undo":
      edit("undo");
      break;
    case "edit_redo":
      edit("redo");
      break;
    case "edit_cut":
      edit("cut");
      break;
    case "edit_copy":
      edit("copy");
      break;
    case "edit_paste":
      edit("paste");
      break;
    case "edit_select_all":
      edit("selectAll");
      break;
    case "format_document":
      void formatActiveDocument();
      break;
    case "palette":
      g.openPalette("commands");
      break;
    case "quickopen":
      g.openPalette("files");
      break;
    case "view_explorer":
      showView("explorer");
      break;
    case "view_search":
      showView("search");
      break;
    case "view_git":
      showView("git");
      break;
    case "view_db":
      showView("db");
      break;
    case "view_artifacts":
      showView("artifacts");
      break;
    case "view_settings":
      showView("settings");
      break;
    case "view_problems":
      g.showProblems();
      break;
    case "tool_csv_schema":
      void g.runCsvSchema();
      break;
    case "tool_csv_to_sql":
      void g.runCsvToSql();
      break;
    case "tool_dump_analyze":
      void g.runDumpAnalyze();
      break;
    case "tool_clean_dump":
      g.openCleanDump();
      break;
    case "tool_data_tools":
      g.openDataTools();
      break;
    case "tool_save_artifact":
      void g.saveActiveAsArtifact("file");
      break;
    case "help_about":
      g.openAbout();
      break;
    case "help_docs":
      void Shell.OpenExternal(`${REPO_URL}#readme`);
      break;
    case "help_issues":
      void Shell.OpenExternal(`${REPO_URL}/issues`);
      break;
    case "toggle_sidebar":
      g.toggleSidebar();
      break;
    case "toggle_panel":
      g.togglePanel();
      break;
    case "toggle_assistant":
      g.toggleAssistant();
      break;
  }
}
