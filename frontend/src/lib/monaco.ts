// Monaco bootstrap: bundle the workers (offline desktop — no CDN), register a
// Novera dark theme matching our tokens, and point @monaco-editor/react at this
// local instance via loader.config so it never fetches Monaco from the network.
import * as monaco from "monaco-editor";
import { loader } from "@monaco-editor/react";

import editorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import jsonWorker from "monaco-editor/esm/vs/language/json/json.worker?worker";
import cssWorker from "monaco-editor/esm/vs/language/css/css.worker?worker";
import htmlWorker from "monaco-editor/esm/vs/language/html/html.worker?worker";
import tsWorker from "monaco-editor/esm/vs/language/typescript/ts.worker?worker";

(self as unknown as { MonacoEnvironment: monaco.Environment }).MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    switch (label) {
      case "json":
        return new jsonWorker();
      case "css":
      case "scss":
      case "less":
        return new cssWorker();
      case "html":
      case "handlebars":
      case "razor":
        return new htmlWorker();
      case "typescript":
      case "javascript":
        return new tsWorker();
      default:
        return new editorWorker();
    }
  },
};

monaco.editor.defineTheme("novera-dark", {
  base: "vs-dark",
  inherit: true,
  rules: [
    { token: "comment", foreground: "6e7681", fontStyle: "italic" },
    { token: "keyword", foreground: "ff7b72" },
    { token: "string", foreground: "a5d6ff" },
    { token: "number", foreground: "79c0ff" },
    { token: "type", foreground: "ffa657" },
    { token: "function", foreground: "d2a8ff" },
    { token: "variable", foreground: "c9d1d9" },
  ],
  colors: {
    "editor.background": "#0d1117",
    "editor.foreground": "#c9d1d9",
    "editorLineNumber.foreground": "#484f58",
    "editorLineNumber.activeForeground": "#8b949e",
    "editor.selectionBackground": "#264f78",
    "editor.lineHighlightBackground": "#161b22",
    "editorCursor.foreground": "#4c8dff",
    "editorWidget.background": "#161b22",
    "editorWidget.border": "#30363d",
    "editorGutter.background": "#0d1117",
    "editorIndentGuide.background1": "#1c222b",
    "scrollbarSlider.background": "#2d334080",
  },
});

loader.config({ monaco });

export { monaco };
