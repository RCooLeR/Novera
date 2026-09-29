import { useEffect, useState } from "react";
import { Check, Folder, List, ListPlus, ListTree, Minus, Plus } from "lucide-react";
import { useStore } from "../state/store";
import type { GitFileChange } from "../lib/services";

function baseName(p: string): string {
  const parts = p.split("/").filter(Boolean);
  return parts[parts.length - 1] || p;
}
function dirName(p: string): string {
  const i = p.lastIndexOf("/");
  return i > 0 ? p.slice(0, i) : "";
}
function summaryColor(summary: string): string {
  switch (summary) {
    case "modified":
      return "var(--git-modified)";
    case "added":
    case "untracked":
      return "var(--git-added)";
    case "deleted":
      return "var(--git-deleted)";
    case "renamed":
      return "var(--purple)";
    default:
      return "var(--text-muted)";
  }
}
function badge(summary: string): string {
  switch (summary) {
    case "modified":
      return "M";
    case "added":
      return "A";
    case "untracked":
      return "U";
    case "deleted":
      return "D";
    case "renamed":
      return "R";
    default:
      return "•";
  }
}

interface TNode {
  name: string;
  path: string;
  change?: GitFileChange;
  children: TNode[];
}
type FlatRow = { kind: "dir" | "file"; name: string; path: string; depth: number; change?: GitFileChange };

function buildTree(changes: GitFileChange[]): TNode[] {
  const root: TNode = { name: "", path: "", children: [] };
  for (const c of changes) {
    const parts = c.path.split("/").filter(Boolean);
    let node = root;
    parts.forEach((seg, i) => {
      const leaf = i === parts.length - 1;
      let child = node.children.find((n) => n.name === seg && (leaf ? !!n.change : !n.change));
      if (!child) {
        child = { name: seg, path: parts.slice(0, i + 1).join("/"), children: [], change: leaf ? c : undefined };
        node.children.push(child);
      }
      node = child;
    });
  }
  return root.children;
}

function flatten(nodes: TNode[], depth: number, out: FlatRow[]) {
  const sorted = [...nodes].sort((a, b) => {
    const ad = !a.change;
    const bd = !b.change;
    if (ad !== bd) return ad ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  for (const n of sorted) {
    if (n.change) out.push({ kind: "file", name: n.name, path: n.path, depth, change: n.change });
    else {
      out.push({ kind: "dir", name: n.name, path: n.path, depth });
      flatten(n.children, depth + 1, out);
    }
  }
}

function FileRow({ change, staged, indent }: { change: GitFileChange; staged: boolean; indent: number }) {
  const openDiff = useStore((s) => s.openDiff);
  const stageFile = useStore((s) => s.stageFile);
  const unstageFile = useStore((s) => s.unstageFile);
  const gitBusy = useStore((s) => s.gitBusy);
  const workspaceTransitioning = useStore((s) => s.workspaceTransitioning);
  const controlsDisabled = gitBusy || workspaceTransitioning;
  return (
    <div className="sc__row"
      title={change.path}
      style={indent ? { paddingLeft: 8 + indent * 14 } : undefined}
    >
      <button
        type="button"
        className="sc__open"
        onClick={() => void openDiff(change.path, change.oldPath, staged)}
        aria-label={`Open ${staged ? "staged" : "unstaged"} ${change.summary} diff for ${change.path}`}
      >
        <span className="sc__name">{baseName(change.path)}</span>
        {indent === 0 && <span className="sc__dir">{dirName(change.path)}</span>}
        {indent > 0 && <span className="sc__dir" />}
        <span className="sc__badge" style={{ color: summaryColor(change.summary) }} aria-hidden="true">
          {badge(change.summary)}
        </span>
      </button>
      <span className="sc__rowactions">
        {staged ? (
          <button className="icon-btn" title="Unstage" disabled={controlsDisabled} onClick={() => void unstageFile(change.path)}>
            <Minus size={14} />
          </button>
        ) : (
          <button className="icon-btn" title="Stage" disabled={controlsDisabled} onClick={() => void stageFile(change.path)}>
            <Plus size={14} />
          </button>
        )}
      </span>
    </div>
  );
}

function Section({ changes, staged, tree }: { changes: GitFileChange[]; staged: boolean; tree: boolean }) {
  if (!tree) {
    return (
      <>
        {changes.map((c) => (
          <FileRow key={`${staged}:${c.path}`} change={c} staged={staged} indent={0} />
        ))}
      </>
    );
  }
  const rows: FlatRow[] = [];
  flatten(buildTree(changes), 0, rows);
  return (
    <>
      {rows.map((r) =>
        r.kind === "dir" ? (
          <div key={`d:${r.path}`} className="sc__dirrow" style={{ paddingLeft: 8 + r.depth * 14 }}>
            <Folder size={13} color="var(--accent)" />
            <span>{r.name}</span>
          </div>
        ) : (
          <FileRow key={`${staged}:${r.path}`} change={r.change!} staged={staged} indent={r.depth} />
        ),
      )}
    </>
  );
}

export default function SourceControl() {
  const gitStatus = useStore((s) => s.gitStatus);
  const loadGitStatus = useStore((s) => s.loadGitStatus);
  const stageAll = useStore((s) => s.stageAll);
  const commit = useStore((s) => s.commit);
  const gitBusy = useStore((s) => s.gitBusy);
  const workspaceTransitioning = useStore((s) => s.workspaceTransitioning);
  const [message, setMessage] = useState("");
  const [tree, setTree] = useState(false);
  const controlsDisabled = gitBusy || workspaceTransitioning;

  useEffect(() => {
    if (!gitStatus) void loadGitStatus();
  }, [gitStatus, loadGitStatus]);

  if (!gitStatus) {
    return <div className="sc__empty">Loading…</div>;
  }
  if (!gitStatus.available) {
    return <div className="sc__empty">{gitStatus.message || "Not a Git repository."}</div>;
  }

  const staged = gitStatus.staged ?? [];
  const unstaged = gitStatus.unstaged ?? [];

  const doCommit = async () => {
    const full = message.trim();
    if (!full || controlsDisabled) return;
    // Git convention: first line is the subject, the remainder (after the first
    // newline) is the body — so the textarea's extra lines aren't discarded.
    const nl = full.indexOf("\n");
    const subject = (nl >= 0 ? full.slice(0, nl) : full).trim();
    const body = nl >= 0 ? full.slice(nl + 1).trim() : "";
    if (!subject) return;
    const ok = await commit(subject, body);
    if (ok) setMessage("");
  };

  return (
    <div className="sc">
      <div className="sc__commit">
        <textarea
          className="sc__msg"
          placeholder={`Message (commit on ${gitStatus.branch})`}
          value={message}
          disabled={controlsDisabled}
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={(e) => {
            if ((e.ctrlKey || e.metaKey) && e.key === "Enter") void doCommit();
          }}
        />
        <div className="sc__commitbar">
          <button
            className="btn btn--primary"
            disabled={!message.trim() || staged.length === 0 || controlsDisabled}
            onClick={() => void doCommit()}
          >
            <Check size={15} /> {gitBusy ? "Working…" : `Commit (${staged.length})`}
          </button>
          <button
            className="icon-btn"
            title={tree ? "Show as list" : "Show as tree"}
            onClick={() => setTree((t) => !t)}
          >
            {tree ? <List size={15} /> : <ListTree size={15} />}
          </button>
        </div>
      </div>

      {staged.length > 0 && (
        <>
          <div className="sc__section-title">
            <span>Staged Changes</span>
            <span className="sc__count">{staged.length}</span>
          </div>
          <Section changes={staged} staged tree={tree} />
        </>
      )}

      <div className="sc__section-title">
        <span>Changes</span>
        <span style={{ display: "flex", alignItems: "center", gap: 6 }}>
          {unstaged.length > 0 && (
            <button className="icon-btn" title="Stage all" disabled={controlsDisabled} onClick={() => void stageAll()}>
              <ListPlus size={14} />
            </button>
          )}
          <span className="sc__count">{unstaged.length}</span>
        </span>
      </div>
      {unstaged.length === 0 && staged.length === 0 ? (
        <div className="sc__empty">No changes</div>
      ) : (
        <Section changes={unstaged} staged={false} tree={tree} />
      )}
    </div>
  );
}
