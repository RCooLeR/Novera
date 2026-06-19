import { Bot, FolderOpen, PanelLeft } from "lucide-react";
import { useStore } from "../state/store";

export default function TitleBar() {
  const wsName = useStore((s) => s.wsName);
  const isOpen = useStore((s) => s.isOpen);
  const toggleSidebar = useStore((s) => s.toggleSidebar);
  const toggleAssistant = useStore((s) => s.toggleAssistant);
  const pickAndOpen = useStore((s) => s.pickAndOpen);

  return (
    <div className="titlebar">
      <div className="titlebar__brand no-drag">
        <img src="/novera-logo.png" alt="Novera" />
        Novera
      </div>
      {isOpen && (
        <button
          className="titlebar__btn no-drag"
          title="Toggle sidebar (Ctrl+B)"
          aria-label="Toggle sidebar"
          onClick={toggleSidebar}
        >
          <PanelLeft size={14} aria-hidden focusable={false} />
        </button>
      )}
      {isOpen && <span className="titlebar__ws">{wsName}</span>}
      <div className="titlebar__spacer" />
      {isOpen && (
        <button
          className="titlebar__btn no-drag"
          title="Toggle assistant"
          aria-label="Toggle assistant"
          onClick={toggleAssistant}
        >
          <Bot size={14} aria-hidden focusable={false} />
        </button>
      )}
      <button className="titlebar__btn no-drag" aria-label="Open folder" onClick={() => void pickAndOpen()}>
        <FolderOpen size={14} aria-hidden focusable={false} /> Open Folder
      </button>
    </div>
  );
}
