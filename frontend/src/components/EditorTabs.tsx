import { useRef } from "react";
import { X } from "lucide-react";
import { nextLinearIndex, type LinearNavigationKey } from "../lib/keyboardNavigation";
import { useStore } from "../state/store";

export const editorTabId = (index: number) => `editor-tab-${index}`;

export default function EditorTabs() {
  const tabs = useStore((s) => s.tabs);
  const activePath = useStore((s) => s.activePath);
  const setActive = useStore((s) => s.setActive);
  const requestCloseTab = useStore((s) => s.requestCloseTab);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const navigate = (index: number, key: LinearNavigationKey) => {
    const next = nextLinearIndex(index, tabs.length, key);
    if (next < 0) return;
    setActive(tabs[next].path);
    tabRefs.current[next]?.focus();
  };

  return (
    <div className="tabs" role="tablist" aria-label="Open editors">
      {tabs.map((t, index) => {
        const dirty = t.kind === "file" && t.content !== t.savedContent;
        const active = t.path === activePath;
        return (
          <div
            key={t.path}
            className={`tab ${active ? "active" : ""} ${dirty ? "tab--dirty" : ""}`}
            title={t.path}
            onAuxClick={(e) => {
              if (e.button === 1) {
                e.preventDefault();
                requestCloseTab(t.path);
              }
            }}
          >
            <button
              ref={(element) => {
                tabRefs.current[index] = element;
              }}
              id={editorTabId(index)}
              className="tab__select"
              role="tab"
              aria-selected={active}
              aria-controls="editor-tabpanel"
              aria-label={`${t.name}${dirty ? ", unsaved changes" : ""}`}
              tabIndex={active ? 0 : -1}
              onClick={() => setActive(t.path)}
              onKeyDown={(event) => {
                if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
                  event.preventDefault();
                  navigate(index, event.key as LinearNavigationKey);
                } else if (event.key === "Delete") {
                  event.preventDefault();
                  requestCloseTab(t.path);
                }
              }}
            >
              <span className="tab__name">{t.name}</span>
              {dirty && <span className="tab__dot" aria-hidden="true" />}
            </button>
            <button
              className="tab__close"
              aria-label={`Close ${t.name}${dirty ? ", unsaved changes" : ""}`}
              title={`Close ${t.name}`}
              onClick={(e) => {
                e.stopPropagation();
                requestCloseTab(t.path);
              }}
            >
              <X size={13} aria-hidden="true" />
            </button>
          </div>
        );
      })}
    </div>
  );
}
