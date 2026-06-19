import { Clock, FolderOpen } from "lucide-react";
import { useStore } from "../state/store";
import { errMessage } from "../lib/services";

function baseName(p: string): string {
  const parts = p.replace(/\\/g, "/").split("/").filter(Boolean);
  return parts[parts.length - 1] || p;
}

export default function Welcome() {
  const pickAndOpen = useStore((s) => s.pickAndOpen);
  const openWorkspace = useStore((s) => s.openWorkspace);
  const recents = useStore((s) => s.recents);
  const setStatus = useStore((s) => s.setStatus);

  const openRecent = (path: string) => {
    // openWorkspace rejects if the folder was moved/deleted — surface it instead
    // of an unhandled promise rejection.
    openWorkspace(path).catch((e) => setStatus(errMessage(e), "error"));
  };

  return (
    <div className="welcome">
      <div className="welcome__card">
        <img className="welcome__logo" src="/novera-logo.png" alt="Novera" />
        <div className="welcome__title">Novera</div>
        <div className="welcome__sub">Local-first AI workbench</div>
        <div className="welcome__actions">
          <button className="btn btn--primary" onClick={() => void pickAndOpen()}>
            <FolderOpen size={16} /> Open Folder
          </button>
        </div>
        {recents.length > 0 && (
          <div className="welcome__recents">
            <h4>Recent</h4>
            {recents.slice(0, 6).map((r) => (
              <button key={r} className="welcome__recent" onClick={() => openRecent(r)} title={r}>
                <Clock size={14} />
                <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {baseName(r)}
                </span>
                <small style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 220 }}>
                  {r}
                </small>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
