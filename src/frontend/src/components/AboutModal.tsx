import { useEffect, useRef, useState } from "react";
import { useStore } from "../state/store";
import { errMessage, Shell } from "../lib/services";
import { buildIdentityText, type BuildIdentity } from "./aboutBuildIdentity";
import { useDialogFocus } from "../lib/useDialogFocus";

const REPO_URL = "https://github.com/RCooLeR/Novera";

// Help → About Novera. Build identity is read from the running executable, not
// duplicated from package metadata that may describe a different artifact.
export default function AboutModal() {
  const open = useStore((s) => s.aboutOpen);
  const close = useStore((s) => s.closeAbout);
  const [build, setBuild] = useState<BuildIdentity | null>(null);
  const [error, setError] = useState("");
  const closeRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useDialogFocus(open, close, closeRef);

  useEffect(() => {
    if (!open) return;
    let current = true;
    setBuild(null);
    setError("");
    void Shell.BuildInfo()
      .then((info) => {
        if (current) setBuild(info);
      })
      .catch((cause: unknown) => {
        if (current) setError(`Build metadata unavailable: ${errMessage(cause)}`);
      });
    return () => {
      current = false;
    };
  }, [open]);

  if (!open) return null;

  const openDocumentation = async () => {
    setError("");
    try {
      await Shell.OpenExternal(`${REPO_URL}#readme`);
    } catch (cause) {
      setError(`Could not open documentation: ${errMessage(cause)}`);
    }
  };

  return (
    <div className="modal-overlay" onClick={close}>
      <div
        ref={dialogRef}
        className="modal about"
        role="dialog"
        aria-modal="true"
        aria-label="About Novera"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="about__head">
          <img className="about__logo" src="/novera-logo.png" alt="" />
          <div>
            <div className="about__name">Novera</div>
            <div className="about__tag">Local-first AI workbench</div>
          </div>
        </div>
        <p className="about__text">
          Code, data files, SQL dumps, databases, terminals, and local or OpenAI-compatible LLMs in one desktop app. A
          bounded, windowed engine handles large files without sending whole-file content across the Go/JavaScript
          bridge, with line numbers, in-file search, hex view, follow-tail, syntax highlighting, guarded editing, and
          CSV/SQL tools. Actual limits depend on file structure, operation, storage, memory, and available disk space.
        </p>
        <p className="about__meta">{build ? buildIdentityText(build) : "Loading build identity…"}</p>
        {build && (
          <p className="about__meta">
            {build.goVersion} · Wails {build.wailsVersion} · React + TypeScript
          </p>
        )}
        {error && (
          <div className="alert alert--error" role="alert">
            {error}
          </div>
        )}
        <div className="modal__actions">
          <button className="btn" onClick={() => void openDocumentation()}>
            Documentation
          </button>
          <button ref={closeRef} className="btn btn--primary" onClick={close}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
