import { useStore } from "../state/store";
import { Shell } from "../lib/services";

const REPO_URL = "https://github.com/RCooLeR/Novera";

// Help → About Novera. A small product/credits dialog.
export default function AboutModal() {
  const open = useStore((s) => s.aboutOpen);
  const close = useStore((s) => s.closeAbout);
  if (!open) return null;

  return (
    <div className="modal-overlay" onClick={close}>
      <div className="modal about" onClick={(e) => e.stopPropagation()}>
        <div className="about__head">
          <img className="about__logo" src="/novera-logo.png" alt="" />
          <div>
            <div className="about__name">Novera</div>
            <div className="about__tag">Local-first AI workbench</div>
          </div>
        </div>
        <p className="about__text">
          Code, data files, SQL dumps, databases, terminals, and local or OpenAI-compatible LLMs in one desktop app. A
          streaming engine opens and edits files of <em>any</em> size — windowed viewing with line numbers, in-file
          search, hex view, follow-tail, syntax highlighting, and crash-safe editing — plus a CSV/SQL data-tools suite.
        </p>
        <p className="about__meta">Wails v3 · React + TypeScript · Go</p>
        <div className="modal__actions">
          <button className="btn" onClick={() => void Shell.OpenExternal(`${REPO_URL}#readme`)}>
            Documentation
          </button>
          <button className="btn btn--primary" onClick={close}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
