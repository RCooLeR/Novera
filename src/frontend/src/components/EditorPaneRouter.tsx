import { lazy, Suspense, type ReactNode } from "react";
import { useStore } from "../state/store";
import BigFileView from "./BigFileView";
import DbQueryView from "./DbQueryView";
import TableView from "./TableView";

// This module deliberately contains no Monaco imports. Text and diff tabs are
// the only routes that request the heavyweight editor chunk.
const MonacoEditorPane = lazy(() => import("./EditorPane"));

export default function EditorPaneRouter() {
  const tabs = useStore((state) => state.tabs);
  const activePath = useStore((state) => state.activePath);
  const root = useStore((state) => state.root);
  const tab = tabs.find((item) => item.path === activePath) ?? null;
  const largeTabs = tabs.filter((item) => item.kind === "file" && (item.tooLarge || item.binary));
  const activeIsLarge = !!tab && tab.kind === "file" && (tab.tooLarge || tab.binary);

  let activePane: ReactNode;
  if (!tab) {
    activePane = <div className="editor-placeholder">Select a file from the explorer to start editing</div>;
  } else if (tab.kind === "db" && tab.connId) {
    activePane = <DbQueryView key={tab.connId} connId={tab.connId} />;
  } else if (tab.kind === "table" && tab.rel) {
    activePane = <TableView key={tab.rel} rel={tab.rel} sourceVersion={tab.sourceVersion} />;
  } else if (tab.kind === "diff" && tab.binary) {
    activePane = <div className="editor-placeholder">{tab.name} is binary — diff not shown.</div>;
  } else if (activeIsLarge) {
    activePane = null;
  } else {
    activePane = (
      <Suspense fallback={<div className="editor-placeholder" role="status">Loading text editor…</div>}>
        <MonacoEditorPane />
      </Suspense>
    );
  }

  return (
    <>
      {largeTabs.map((largeTab) => {
        const active = largeTab.path === tab?.path;
        const absolutePath = root ? `${root}/${largeTab.path}` : largeTab.path;
        return (
          <div key={largeTab.instanceId} hidden={!active} style={active ? { display: "contents" } : undefined}>
            <BigFileView
              abs={absolutePath}
              name={largeTab.name}
              tabPath={largeTab.path}
              binaryHint={largeTab.binary}
              sourceVersion={largeTab.sourceVersion}
              staleOnDisk={largeTab.staleOnDisk}
            />
          </div>
        );
      })}
      {!activeIsLarge && activePane}
    </>
  );
}
