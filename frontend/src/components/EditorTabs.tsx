import { X } from "lucide-react";
import { useStore } from "../state/store";

export default function EditorTabs() {
  const tabs = useStore((s) => s.tabs);
  const activePath = useStore((s) => s.activePath);
  const setActive = useStore((s) => s.setActive);
  const requestCloseTab = useStore((s) => s.requestCloseTab);

  return (
    <div className="tabs">
      {tabs.map((t) => {
        const dirty = t.kind === "file" && t.content !== t.savedContent;
        return (
          <div
            key={t.path}
            className={`tab ${t.path === activePath ? "active" : ""} ${dirty ? "tab--dirty" : ""}`}
            title={t.path}
            onClick={() => setActive(t.path)}
            onAuxClick={(e) => {
              if (e.button === 1) {
                e.preventDefault();
                requestCloseTab(t.path);
              }
            }}
          >
            <span className="tab__name">{t.name}</span>
            {/* The dot (unsaved) and the close button overlap; CSS swaps the dot
                for the X on hover, so a dirty tab is always closable. */}
            {dirty && <span className="tab__dot" title="Unsaved changes" />}
            <button
              className="tab__close"
              title="Close"
              onClick={(e) => {
                e.stopPropagation();
                requestCloseTab(t.path);
              }}
            >
              <X size={13} />
            </button>
          </div>
        );
      })}
    </div>
  );
}
