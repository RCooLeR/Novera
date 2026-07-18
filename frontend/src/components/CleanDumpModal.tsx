import { useEffect, useRef, useState } from "react";
import { Wand2, X } from "lucide-react";
import { useStore } from "../state/store";
import { useDialogFocus } from "../lib/useDialogFocus";

// Form for the "Clean SQL dump" tool: pick cleanup presets + an output path.
export default function CleanDumpModal() {
  const target = useStore((s) => s.cleanDump);
  const busy = useStore((s) => s.toolBusy);
  const cancel = useStore((s) => s.cancelCleanDump);
  const apply = useStore((s) => s.applyCleanDump);

  const [removeDefiner, setRemoveDefiner] = useState(true);
  const [dropAutoIncrement, setDropAutoIncrement] = useState(false);
  const [engine, setEngine] = useState("");
  const [charset, setCharset] = useState("");
  const [collation, setCollation] = useState("");
  const [fromDatabase, setFromDatabase] = useState("");
  const [toDatabase, setToDatabase] = useState("");
  const [out, setOut] = useState("");
  const closeRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useDialogFocus(!!target, cancel, closeRef);

  useEffect(() => {
    if (!target) return;
    const base = (target.rel.split("/").pop() ?? target.rel).replace(/\.[^.]+$/, "");
    const dir = target.rel.includes("/") ? target.rel.slice(0, target.rel.lastIndexOf("/")) : "";
    setOut(dir ? `${dir}/${base}-clean.sql` : `${base}-clean.sql`);
    setRemoveDefiner(true);
    setDropAutoIncrement(false);
    setEngine("");
    setCharset("");
    setCollation("");
    setFromDatabase("");
    setToDatabase("");
  }, [target, cancel]);

  if (!target) return null;

  const run = () =>
    void apply(target.rel, out.trim(), {
      removeDefiner,
      dropAutoIncrement,
      engine: engine.trim(),
      charset: charset.trim(),
      collation: collation.trim(),
      fromDatabase: fromDatabase.trim(),
      toDatabase: toDatabase.trim(),
    });

  const name = target.rel.split("/").pop() ?? target.rel;

  return (
    <div className="modal-overlay" onMouseDown={cancel}>
      <div ref={dialogRef} className="modal toolsmodal" role="dialog" aria-modal="true" aria-label="Clean SQL dump" onMouseDown={(e) => e.stopPropagation()}>
        <div className="toolsmodal__head">
          <span className="modal__title">Clean SQL dump · {name}</span>
          <button ref={closeRef} className="icon-btn" title="Close" aria-label="Close" onClick={cancel}>
            <X size={15} aria-hidden focusable={false} />
          </button>
        </div>
        <div className="toolsmodal__body">
          <div className="cleandump__checks">
            <label>
              <input type="checkbox" checked={removeDefiner} onChange={(e) => setRemoveDefiner(e.target.checked)} /> Remove DEFINER clauses
            </label>
            <label>
              <input type="checkbox" checked={dropAutoIncrement} onChange={(e) => setDropAutoIncrement(e.target.checked)} /> Drop AUTO_INCREMENT= table options
            </label>
          </div>
          <div className="toolsmodal__form cleandump__grid">
            <label>
              ENGINE →
              <input value={engine} spellCheck={false} placeholder="leave blank to keep" onChange={(e) => setEngine(e.target.value)} />
            </label>
            <label>
              CHARSET →
              <input value={charset} spellCheck={false} placeholder="e.g. utf8mb4" onChange={(e) => setCharset(e.target.value)} />
            </label>
            <label>
              COLLATE →
              <input value={collation} spellCheck={false} placeholder="optional" onChange={(e) => setCollation(e.target.value)} />
            </label>
            <label>
              Rename DB
              <input value={fromDatabase} spellCheck={false} placeholder="from" onChange={(e) => setFromDatabase(e.target.value)} />
            </label>
            <label>
              → to
              <input value={toDatabase} spellCheck={false} placeholder="to" onChange={(e) => setToDatabase(e.target.value)} />
            </label>
          </div>
          <div className="toolsmodal__form">
            <label style={{ flex: 2 }}>
              Output file
              <input value={out} spellCheck={false} onChange={(e) => setOut(e.target.value)} />
            </label>
            <button className="btn btn--primary" disabled={busy || !out.trim()} onClick={run}>
              <Wand2 size={13} /> Clean → file
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
