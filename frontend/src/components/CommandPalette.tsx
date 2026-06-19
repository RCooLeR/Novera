import { useEffect, useMemo, useRef, useState } from "react";
import { File as FileIcon, ChevronRight } from "lucide-react";
import { useStore } from "../state/store";
import { formatActiveDocument } from "../lib/editorBridge";

interface Command {
  id: string;
  title: string;
  run: () => void;
}

// Subsequence fuzzy match: returns a score (higher = better) or -1 if no match.
function fuzzyScore(query: string, target: string): number {
  if (!query) return 0;
  const q = query.toLowerCase();
  const t = target.toLowerCase();
  let qi = 0;
  let score = 0;
  let streak = 0;
  let prevIdx = -1;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) {
      // Only award a consecutive-run bonus for an actual run — guard against the
      // first matched char (prevIdx === -1) spuriously satisfying prevIdx === ti-1.
      streak = prevIdx >= 0 && prevIdx === ti - 1 ? streak + 1 : 0;
      score += 1 + streak * 2 + (ti === 0 || "/._- ".includes(t[ti - 1]) ? 3 : 0);
      prevIdx = ti;
      qi++;
    }
  }
  if (qi < q.length) return -1;
  return score - t.length * 0.02; // mild preference for shorter targets
}

// Cheap subsequence test used to reject non-matches before the (costlier)
// fuzzyScore pass. Both operands must already be lowercased.
function isSubsequence(q: string, t: string): boolean {
  if (!q) return true;
  let qi = 0;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) qi++;
  }
  return qi === q.length;
}

function baseName(p: string): string {
  const i = p.lastIndexOf("/");
  return i >= 0 ? p.slice(i + 1) : p;
}
function dirName(p: string): string {
  const i = p.lastIndexOf("/");
  return i > 0 ? p.slice(0, i) : "";
}

export default function CommandPalette() {
  const open = useStore((s) => s.paletteOpen);
  const mode = useStore((s) => s.paletteMode);
  const allFiles = useStore((s) => s.allFiles);
  const loadingAllFiles = useStore((s) => s.loadingAllFiles);
  const allFilesError = useStore((s) => s.allFilesError);
  const loadAllFiles = useStore((s) => s.loadAllFiles);
  const close = useStore((s) => s.closePalette);

  // Commands call actions imperatively via getState() — no whole-store
  // subscription, so the palette doesn't re-render on every store mutation.
  const [query, setQuery] = useState("");
  const [debTerm, setDebTerm] = useState(""); // debounced file-search term
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (open) {
      setQuery(mode === "commands" ? ">" : "");
      setSel(0);
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [open, mode]);

  const isCommandMode = query.startsWith(">");
  const term = isCommandMode ? query.slice(1).trim() : query.trim();

  // Debounce the file-search term so the full-index scan doesn't run on every
  // keystroke (commands are a tiny list and don't need debouncing).
  useEffect(() => {
    if (isCommandMode) return;
    const id = setTimeout(() => setDebTerm(term), 120);
    return () => clearTimeout(id);
  }, [term, isCommandMode]);

  const commands = useMemo<Command[]>(() => {
    const get = useStore.getState;
    return [
      { id: "file.open", title: "Open Folder…", run: () => void get().pickAndOpen() },
      { id: "file.save", title: "Save File", run: () => void get().saveActive() },
      { id: "editor.format", title: "Format Document", run: () => void formatActiveDocument() },
      { id: "file.new", title: "New File", run: () => get().startNewFile("") },
      { id: "folder.new", title: "New Folder", run: () => get().startNewFolder("") },
      { id: "explorer.refresh", title: "Refresh Explorer", run: () => void get().refreshTree() },
      { id: "view.explorer", title: "Show: Explorer", run: () => get().setView("explorer") },
      { id: "view.git", title: "Show: Source Control", run: () => get().setView("git") },
      { id: "view.db", title: "Show: Database", run: () => get().setView("db") },
      { id: "view.problems", title: "Show: Problems", run: () => get().showProblems() },
      { id: "git.refresh", title: "Source Control: Refresh", run: () => void get().loadGitStatus() },
      { id: "diag.run", title: "Problems: Rescan Workspace", run: () => void get().runDiagnostics() },
      { id: "view.sidebar", title: "Toggle Sidebar", run: () => get().toggleSidebar() },
      { id: "view.panel", title: "Toggle Terminal Panel", run: () => get().togglePanel() },
      { id: "view.assistant", title: "Toggle Assistant", run: () => get().toggleAssistant() },
      { id: "chat.clear", title: "Assistant: Clear Conversation", run: () => get().clearChat() },
    ];
  }, []);

  const fileResults = useMemo(() => {
    if (isCommandMode) return [];
    const q = debTerm.toLowerCase();
    if (!q) return allFiles.slice(0, 50); // empty query: just show the first 50
    const scored: { f: string; score: number }[] = [];
    for (const f of allFiles) {
      // Cheap reject before the costlier scoring pass.
      if (!isSubsequence(q, f.toLowerCase())) continue;
      const score = fuzzyScore(debTerm, f);
      if (score >= 0) scored.push({ f, score });
    }
    scored.sort((a, b) => b.score - a.score);
    return scored.slice(0, 50).map((r) => r.f);
  }, [allFiles, debTerm, isCommandMode]);

  const commandResults = useMemo(() => {
    if (!isCommandMode) return [];
    const scored = commands
      .map((c) => ({ c, score: fuzzyScore(term, c.title) }))
      .filter((r) => r.score >= 0);
    scored.sort((a, b) => b.score - a.score);
    return scored.map((r) => r.c);
  }, [commands, term, isCommandMode]);

  const count = isCommandMode ? commandResults.length : fileResults.length;

  useEffect(() => {
    if (sel >= count) setSel(count > 0 ? count - 1 : 0);
  }, [count, sel]);

  if (!open) return null;

  const choose = (index: number) => {
    if (isCommandMode) {
      const cmd = commandResults[index];
      if (cmd) {
        close();
        cmd.run();
      }
    } else {
      const file = fileResults[index];
      if (file) {
        close();
        void useStore.getState().openFile(file, baseName(file));
      }
    }
  };

  return (
    <div className="palette-overlay" onMouseDown={close}>
      <div className="palette" onMouseDown={(e) => e.stopPropagation()}>
        <input
          ref={inputRef}
          className="palette__input"
          value={query}
          spellCheck={false}
          placeholder={isCommandMode ? "Type a command…" : "Go to file…  (prefix with > for commands)"}
          onChange={(e) => {
            setQuery(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((i) => Math.min(i + 1, Math.max(count - 1, 0)));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((i) => Math.max(i - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              choose(sel);
            } else if (e.key === "Escape") {
              e.preventDefault();
              close();
            }
          }}
        />
        <div className="palette__list">
          {count === 0 &&
            (isCommandMode ? (
              <div className="palette__empty">No matches</div>
            ) : loadingAllFiles ? (
              <div className="palette__empty">Indexing files…</div>
            ) : allFilesError ? (
              <div className="palette__empty">
                Couldn't index files.{" "}
                <button className="btn btn--link" onMouseDown={(e) => e.preventDefault()} onClick={() => void loadAllFiles()}>
                  Retry
                </button>
              </div>
            ) : (
              <div className="palette__empty">No matches</div>
            ))}
          {isCommandMode
            ? commandResults.map((c, i) => (
                <div
                  key={c.id}
                  className={`palette__item ${i === sel ? "selected" : ""}`}
                  onMouseEnter={() => setSel(i)}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    choose(i);
                  }}
                >
                  <ChevronRight size={14} className="palette__cmdicon" />
                  <span className="palette__primary">{c.title}</span>
                </div>
              ))
            : fileResults.map((f, i) => (
                <div
                  key={f}
                  className={`palette__item ${i === sel ? "selected" : ""}`}
                  onMouseEnter={() => setSel(i)}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    choose(i);
                  }}
                  title={f}
                >
                  <FileIcon size={14} className="palette__cmdicon" />
                  <span className="palette__primary">{baseName(f)}</span>
                  <span className="palette__secondary">{dirName(f)}</span>
                </div>
              ))}
        </div>
      </div>
    </div>
  );
}
