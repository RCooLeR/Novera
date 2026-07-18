import { useEffect, useRef, useState } from "react";
import Editor, { DiffEditor } from "@monaco-editor/react";
import type { OnMount } from "@monaco-editor/react";
import { monaco } from "../lib/monaco";
import { useStore } from "../state/store";
import { setActiveEditor } from "../lib/editorBridge";
import DbQueryView from "./DbQueryView";
import TableView from "./TableView";
import BigFileView from "./BigFileView";
import ConfirmModal from "./ConfirmModal";
import { errMessage, Shell } from "../lib/services";

const MONO = "Cascadia Code, JetBrains Mono, Consolas, monospace";

type EditorInstance = Parameters<OnMount>[0];

function applyReveal(ed: EditorInstance, line: number, column: number) {
  ed.revealLineInCenter(line);
  ed.setPosition({ lineNumber: line, column: Math.max(column, 1) });
  ed.focus();
}

export default function EditorPane() {
  const tab = useStore((s) => s.tabs.find((t) => t.path === s.activePath) ?? null);
  const root = useStore((s) => s.root);
  const updateContent = useStore((s) => s.updateContent);
  const saveActive = useStore((s) => s.saveActive);
  const overwriteStaleTab = useStore((s) => s.overwriteStaleTab);
  const reloadTab = useStore((s) => s.reloadTab);
  const setStatus = useStore((s) => s.setStatus);
  const settings = useStore((s) => s.settings);
  const pendingReveal = useStore((s) => s.pendingReveal);
  const clearReveal = useStore((s) => s.clearReveal);
  const editorRef = useRef<EditorInstance | null>(null);
  const [conflictAction, setConflictAction] = useState<"reload" | "overwrite" | null>(null);

  useEffect(() => {
    setConflictAction(null);
  }, [tab?.instanceId]);

  // Keep the editor bridge pointed at a live file editor (cleared for non-files).
  useEffect(() => {
    if (!tab || tab.kind !== "file" || tab.binary || tab.tooLarge) setActiveEditor(null);
  }, [tab]);

  // Drop the bridge handle (and our ref) when the pane unmounts so a later
  // Format command can't run against a disposed editor.
  useEffect(
    () => () => {
      setActiveEditor(null);
      editorRef.current = null;
    },
    [],
  );

  // Reveal a line/column when search (or anything) requests it on the active file.
  useEffect(() => {
    if (!pendingReveal || !tab || tab.kind !== "file" || pendingReveal.path !== tab.path) return;
    const ed = editorRef.current;
    if (!ed) return;
    const { line, column } = pendingReveal;
    const id = setTimeout(() => {
      applyReveal(ed, line, column);
      clearReveal();
    }, 0);
    return () => clearTimeout(id);
  }, [pendingReveal, tab, clearReveal]);

  if (!tab) {
    return <div className="editor-placeholder">Select a file from the explorer to start editing</div>;
  }

  if (tab.kind === "db" && tab.connId) {
    return <DbQueryView key={tab.connId} connId={tab.connId} />;
  }

  if (tab.kind === "table" && tab.rel) {
    return <TableView key={tab.rel} rel={tab.rel} sourceVersion={tab.sourceVersion} />;
  }

  if (tab.kind === "diff") {
    if (tab.binary) {
      return <div className="editor-placeholder">{tab.name} is binary — diff not shown.</div>;
    }
    return (
      <DiffEditor
        theme="novera-dark"
        language={tab.language}
        original={tab.diffOld ?? ""}
        modified={tab.diffNew ?? ""}
        options={{
          readOnly: true,
          renderSideBySide: true,
          automaticLayout: true,
          fontFamily: MONO,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
        }}
      />
    );
  }

  // Too large for Monaco, or binary: stream it through the big-file engine
  // (windowed text with real line numbers + search/goto, or a hex view). The
  // engine opens by absolute path, so resolve it against the workspace root.
  if (tab.tooLarge || tab.binary) {
    const abs = root ? `${root}/${tab.path}` : tab.path;
    return (
      <BigFileView
        key={tab.path}
        abs={abs}
        name={tab.name}
        tabPath={tab.path}
        binaryHint={tab.binary}
        sourceVersion={tab.sourceVersion}
        staleOnDisk={tab.staleOnDisk}
      />
    );
  }

  const onMount: OnMount = (editor) => {
    editorRef.current = editor;
    setActiveEditor(editor);
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      const fmt = useStore.getState().settings?.editor.formatOnSave;
      const action = fmt ? editor.getAction("editor.action.formatDocument") : null;
      if (action) {
        void action.run().finally(() => void useStore.getState().saveActive());
      } else {
        void saveActive();
      }
    });
    const pr = useStore.getState().pendingReveal;
    if (pr && tab && pr.path === tab.path) {
      applyReveal(editor, pr.line, pr.column);
      useStore.getState().clearReveal();
    }
  };

  const saveConflictCopy = async () => {
    const submittedContent = tab.content;
    try {
      const destination = await Shell.SaveTextFile(`${tab.name}.local-copy`, submittedContent);
      if (destination) setStatus(`Saved a copy of the local editor version to ${destination}`, "success");
    } catch (error) {
      setStatus(`Could not save a copy: ${errMessage(error)}`, "error");
    }
  };

  return (
    <div className="editorwrap">
      {tab.staleOnDisk && (
        <div className="disk-banner">
          <span>This file changed on disk. Your editor version is preserved.</span>
          <button className="btn" onClick={() => void saveConflictCopy()}>
            Save Copy…
          </button>
          <button className="btn" onClick={() => setConflictAction("reload")}>
            Reload Disk…
          </button>
          <button className="btn btn--danger" disabled={!tab.staleRevision} onClick={() => setConflictAction("overwrite")}>
            Overwrite Disk…
          </button>
        </div>
      )}
      {conflictAction && (
        <ConfirmModal
          title={conflictAction === "reload" ? "Reload disk version?" : "Overwrite disk version?"}
          body={
            conflictAction === "reload"
              ? "This discards the unsaved editor version. Save a copy first if you may need it."
              : "This replaces only the exact external revision Novera observed. If the file changed again, the overwrite will be refused."
          }
          confirmLabel={conflictAction === "reload" ? "Discard Mine & Reload" : "Overwrite Observed Version"}
          danger
          onConfirm={() => {
            const action = conflictAction;
            setConflictAction(null);
            if (action === "reload") void reloadTab(tab.path);
            else void overwriteStaleTab(tab.path);
          }}
          onCancel={() => setConflictAction(null)}
        />
      )}
      <div className="editorwrap__editor">
        <Editor
          theme="novera-dark"
          path={tab.path}
          language={tab.language}
          value={tab.content}
          onChange={(v) => updateContent(tab.path, v ?? "")}
          onMount={onMount}
          options={{
        fontSize: settings?.editor.fontSize ?? 13,
        tabSize: settings?.editor.tabSize ?? 4,
        insertSpaces: true,
        wordWrap: settings?.editor.wordWrap ? "on" : "off",
        minimap: { enabled: settings?.editor.minimap ?? true, renderCharacters: false },
        fontFamily: MONO,
        fontLigatures: true,
        smoothScrolling: true,
        cursorSmoothCaretAnimation: "on",
        cursorBlinking: "smooth",
        automaticLayout: true,
        scrollBeyondLastLine: false,
        renderWhitespace: "selection",
        renderLineHighlight: "all",
        bracketPairColorization: { enabled: true },
        guides: { bracketPairs: true, indentation: true },
        stickyScroll: { enabled: true },
        linkedEditing: true,
        formatOnPaste: true,
        suggestSelection: "first",
        "semanticHighlighting.enabled": true,
            scrollbar: { verticalScrollbarSize: 11, horizontalScrollbarSize: 11 },
            padding: { top: 10 },
          }}
        />
      </div>
    </div>
  );
}
