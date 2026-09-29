import { useEffect, useState } from "react";
import { AlertTriangle, Archive, ArchiveRestore, FileQuestion, RefreshCw, Trash2 } from "lucide-react";
import { useStore } from "../state/store";

const baseName = (p: string) => p.replace(/\\/g, "/").split("/").filter(Boolean).pop() || p;

// Short relative time, e.g. "3m ago" / "2d ago".
function ago(ms: number): string {
  if (!ms) return "";
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export default function ArtifactsView() {
  const artifacts = useStore((s) => s.artifacts);
  const artifactsError = useStore((s) => s.artifactsError);
  const loadArtifacts = useStore((s) => s.loadArtifacts);
  const setArchived = useStore((s) => s.setArtifactArchived);
  const deleteArtifact = useStore((s) => s.deleteArtifact);
  const openFile = useStore((s) => s.openFile);
  const workspaceTransitioning = useStore((s) => s.workspaceTransitioning);
  const [showArchived, setShowArchived] = useState(false);

  useEffect(() => {
    void loadArtifacts();
  }, [loadArtifacts]);

  const visible = artifacts.filter((a) => a.archived === showArchived);

  return (
    <div className="artifacts">
      <div className="artifacts__bar">
        <button
          className={`artifacts__filter ${!showArchived ? "active" : ""}`}
          onClick={() => setShowArchived(false)}
        >
          Active
        </button>
        <button
          className={`artifacts__filter ${showArchived ? "active" : ""}`}
          onClick={() => setShowArchived(true)}
        >
          Archived
        </button>
        <span style={{ flex: 1 }} />
        <button className="icon-btn" title="Refresh" aria-label="Refresh artifacts" disabled={workspaceTransitioning} onClick={() => void loadArtifacts()}>
          <RefreshCw size={13} />
        </button>
      </div>

      {artifactsError ? (
        <div className="artifacts__empty" role="alert">
          <AlertTriangle size={14} aria-hidden="true" /> {artifactsError}
        </div>
      ) : visible.length === 0 ? (
        <div className="artifacts__empty">
          {showArchived
            ? "No archived artifacts."
            : "No artifacts yet. The agent can register outputs with create_artifact, or save the active file from the editor."}
        </div>
      ) : (
        <div className="artifacts__list">
          {visible.map((a) => (
            <div key={a.id} className="artifacts__item">
              <button
                className="artifacts__main"
                title={a.missing ? "Content file is missing" : `Open ${a.path}`}
                disabled={a.missing || workspaceTransitioning}
                onClick={() => void openFile(a.path, baseName(a.path))}
              >
                <span className={`artifacts__kind kind--${a.kind}`}>{a.kind}</span>
                <span className="artifacts__title">{a.title}</span>
                {a.missing ? (
                  <FileQuestion size={13} className="artifacts__warn" />
                ) : a.stale ? (
                  <AlertTriangle size={13} className="artifacts__warn" />
                ) : null}
              </button>
              <div className="artifacts__meta">
                <span className="artifacts__path">{a.path}</span>
                <span className="artifacts__time">{ago(a.updatedAt)}</span>
              </div>
              {a.sources && a.sources.length > 0 && (
                <div className="artifacts__lineage" title="Derived from">
                  ← {a.sources.join(", ")}
                </div>
              )}
              <div className="artifacts__actions">
                {a.stale && !a.missing && <span className="artifacts__stale">source changed</span>}
                <span style={{ flex: 1 }} />
                <button
                  className="icon-btn"
                  title={a.archived ? "Restore" : "Archive"}
                  aria-label={`${a.archived ? "Restore" : "Archive"} ${a.title}`}
                  disabled={workspaceTransitioning}
                  onClick={() => void setArchived(a.id, !a.archived)}
                >
                  {a.archived ? <ArchiveRestore size={13} /> : <Archive size={13} />}
                </button>
                <button
                  className="icon-btn"
                  title="Remove from registry (keeps the file)"
                  aria-label={`Remove ${a.title} from the artifact registry`}
                  disabled={workspaceTransitioning}
                  onClick={() => void deleteArtifact(a.id)}
                >
                  <Trash2 size={13} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
