import { useEffect, useState } from "react";
import { ChevronFirst, ChevronLast, ChevronLeft, ChevronRight } from "lucide-react";
import { Workspace, errMessage } from "../lib/services";
import type { FileChunk } from "../lib/services";

// Read-only paged viewer for files too large for Monaco (or binary). It loads
// one ~256 KiB byte-window at a time via Workspace.ReadFileRange — the whole
// file never enters memory or a single JS string — so multi-GB files can be
// inspected a page at a time. Text files show as text; binary files as hex.
const PAGE = 256 * 1024;

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

export default function LargeFileView({ rel, name }: { rel: string; name: string }) {
  const [offset, setOffset] = useState(0);
  const [chunk, setChunk] = useState<FileChunk | null>(null);
  const [error, setError] = useState("");

  // Reset to the top when the file changes.
  useEffect(() => {
    setOffset(0);
    setChunk(null);
  }, [rel]);

  useEffect(() => {
    let alive = true;
    setError("");
    Workspace.ReadFileRange(rel, offset, PAGE)
      .then((c) => {
        if (alive) setChunk(c);
      })
      .catch((e) => {
        if (alive) setError(errMessage(e));
      });
    return () => {
      alive = false;
    };
  }, [rel, offset]);

  if (error) return <div className="editor-placeholder">{error}</div>;
  if (!chunk) return <div className="editor-placeholder">Loading…</div>;

  const { total } = chunk;
  const start = chunk.length > 0 ? chunk.offset + 1 : chunk.offset;
  const end = chunk.offset + chunk.length;
  const lastOffset = total === 0 ? 0 : Math.floor((total - 1) / PAGE) * PAGE;
  const atStart = chunk.offset <= 0;
  const atEnd = chunk.eof;

  return (
    <div className="lfv">
      <div className="lfv__bar">
        <span className="lfv__name">{name}</span>
        <span className="lfv__badge">read-only · {chunk.binary ? "binary (hex)" : "text"}</span>
        <span className="lfv__range">
          bytes {start.toLocaleString()}–{end.toLocaleString()} of {total.toLocaleString()} ({fmtBytes(total)})
        </span>
        <span className="lfv__spacer" />
        <button className="icon-btn" title="First page" disabled={atStart} onClick={() => setOffset(0)}>
          <ChevronFirst size={15} />
        </button>
        <button className="icon-btn" title="Previous page" disabled={atStart} onClick={() => setOffset((o) => Math.max(0, o - PAGE))}>
          <ChevronLeft size={15} />
        </button>
        <button className="icon-btn" title="Next page" disabled={atEnd} onClick={() => setOffset((o) => o + PAGE)}>
          <ChevronRight size={15} />
        </button>
        <button className="icon-btn" title="Last page" disabled={atEnd} onClick={() => setOffset(lastOffset)}>
          <ChevronLast size={15} />
        </button>
      </div>
      <pre className="lfv__body">{chunk.binary ? chunk.hex : chunk.text}</pre>
    </div>
  );
}
