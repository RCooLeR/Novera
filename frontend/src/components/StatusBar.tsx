import { useState } from "react";
import { AlertTriangle, CircleAlert, X } from "lucide-react";
import { useStore } from "../state/store";

// Encodings the user can convert a file to from the status bar.
const ENCODINGS: { id: string; label: string }[] = [
  { id: "utf-8", label: "UTF-8" },
  { id: "utf-8-bom", label: "UTF-8 with BOM" },
  { id: "utf-16le", label: "UTF-16 LE" },
  { id: "utf-16be", label: "UTF-16 BE" },
  { id: "latin-1", label: "Latin-1" },
];
const encLabel = (id?: string) => ENCODINGS.find((e) => e.id === id)?.label ?? "UTF-8";

export default function StatusBar() {
  const status = useStore((s) => s.status);
  const isOpen = useStore((s) => s.isOpen);
  const wsName = useStore((s) => s.wsName);
  const tab = useStore((s) => s.tabs.find((t) => t.path === s.activePath) ?? null);
  const gitStatus = useStore((s) => s.gitStatus);
  const diagnostics = useStore((s) => s.diagnostics);
  const showProblems = useStore((s) => s.showProblems);
  const dismissStatus = useStore((s) => s.dismissStatus);
  const convertEncoding = useStore((s) => s.convertEncoding);
  const [encMenu, setEncMenu] = useState(false);

  const enc = tab?.encoding || "utf-8";
  const nonUtf8 = enc !== "utf-8";

  const style =
    status?.kind === "error"
      ? { background: "var(--red)", color: "#fff" }
      : status?.kind === "success"
        ? { background: "var(--green)", color: "#06140a" }
        : undefined;

  const errors = diagnostics?.items.filter((d) => d.severity === "error").length ?? 0;
  const warnings = diagnostics?.items.filter((d) => d.severity !== "error").length ?? 0;

  return (
    <div className={status ? "statusbar" : "statusbar idle"} style={style}>
      {isOpen && gitStatus?.available && (
        <span className="statusbar__item" title="Current branch">
          ⎇ {gitStatus.branch}
        </span>
      )}
      {isOpen && diagnostics && (
        <button className="statusbar__item statusbar__btn" onClick={showProblems} title="Show Problems">
          <CircleAlert size={12} /> {errors}
          <AlertTriangle size={12} style={{ marginLeft: 6 }} /> {warnings}
        </button>
      )}
      <span className="statusbar__item">
        {status ? status.message : isOpen ? wsName : "No folder open"}
      </span>
      {status?.kind === "error" && (
        <button className="statusbar__item statusbar__btn" title="Dismiss" onClick={dismissStatus}>
          <X size={12} />
        </button>
      )}
      <span className="statusbar__spacer" />
      {tab && !tab.binary && !tab.tooLarge && tab.kind === "file" && (
        <span className="statusbar__encwrap">
          <button
            className="statusbar__item statusbar__btn"
            style={nonUtf8 ? { color: "var(--amber)", fontWeight: 600 } : undefined}
            title={
              nonUtf8
                ? `This file is ${encLabel(enc)} (not UTF-8). Click to save it in another encoding.`
                : "File encoding — click to change"
            }
            onClick={() => setEncMenu((o) => !o)}
          >
            {nonUtf8 && <AlertTriangle size={12} style={{ marginRight: 4 }} />}
            {encLabel(enc)}
          </button>
          {encMenu && (
            <>
              <div className="statusbar__encbackdrop" onClick={() => setEncMenu(false)} />
              <div className="statusbar__encmenu">
                <div className="statusbar__encmenu-title">Save with encoding</div>
                {ENCODINGS.map((e) => (
                  <button
                    key={e.id}
                    className={`statusbar__encmenu-item ${e.id === enc ? "active" : ""}`}
                    onClick={() => {
                      setEncMenu(false);
                      if (e.id !== enc) void convertEncoding(tab.path, e.id);
                    }}
                  >
                    {e.label}
                    {e.id === enc ? " ✓" : ""}
                  </button>
                ))}
              </div>
            </>
          )}
        </span>
      )}
      {tab && !tab.binary && !tab.tooLarge && tab.kind === "file" && (
        <span className="statusbar__item" style={{ textTransform: "capitalize" }}>
          {tab.language}
        </span>
      )}
    </div>
  );
}
