import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronRight, Loader2 } from "lucide-react";
import { useStore } from "../state/store";
import type { SearchMatch } from "../lib/services";

function baseName(p: string): string {
  const i = p.lastIndexOf("/");
  return i >= 0 ? p.slice(i + 1) : p;
}
function dirName(p: string): string {
  const i = p.lastIndexOf("/");
  return i > 0 ? p.slice(0, i) : "";
}

export default function Search() {
  const searchQuery = useStore((s) => s.searchQuery);
  const searchResults = useStore((s) => s.searchResults);
  const searching = useStore((s) => s.searching);
  const setSearchQuery = useStore((s) => s.setSearchQuery);
  const runSearch = useStore((s) => s.runSearch);
  const openFileAt = useStore((s) => s.openFileAt);

  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const debounce = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // Reflect each keystroke immediately (responsive input) but only walk the tree
  // after a short pause, so typing doesn't fire a full-workspace scan per key.
  const onChange = (value: string) => {
    setSearchQuery(value);
    if (debounce.current) clearTimeout(debounce.current);
    if (value.trim()) debounce.current = setTimeout(() => void runSearch(value), 220);
  };
  useEffect(() => () => clearTimeout(debounce.current), []);

  // Prune collapse state whenever the result set changes — otherwise a path
  // collapsed in one query stays collapsed (or mis-keyed) across later queries.
  useEffect(() => setCollapsed({}), [searchResults]);

  // Group matches by file, preserving first-seen order.
  const groups = useMemo(() => {
    const map = new Map<string, SearchMatch[]>();
    for (const m of searchResults?.matches ?? []) {
      const arr = map.get(m.path);
      if (arr) arr.push(m);
      else map.set(m.path, [m]);
    }
    return Array.from(map.entries());
  }, [searchResults]);

  return (
    <div className="search">
      <div className="search__box">
        <input
          type="search"
          className="search__input"
          value={searchQuery}
          spellCheck={false}
          aria-label="Search workspace"
          aria-busy={searching}
          placeholder="Search workspace…"
          onChange={(e) => onChange(e.target.value)}
        />
        {searching && <Loader2 size={14} className="spin search__spin" aria-hidden="true" />}
      </div>

      {searchResults && (
        <div className="search__summary" role="status" aria-live="polite">
          {searchResults.matches.length === 0
            ? "No results"
            : `${searchResults.matches.length} result${searchResults.matches.length === 1 ? "" : "s"} in ${searchResults.fileCount} file${searchResults.fileCount === 1 ? "" : "s"}${searchResults.truncated ? " (truncated)" : ""}`}
        </div>
      )}

      <div className="search__results" role="list" aria-label="Workspace search results">
        {groups.map(([path, matches]) => {
          const isCollapsed = collapsed[path];
          return (
            <div key={path} role="listitem">
              <button
                type="button"
                className="search__file"
                onClick={() => setCollapsed((c) => ({ ...c, [path]: !c[path] }))}
                title={path}
                aria-expanded={!isCollapsed}
              >
                {isCollapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
                <span className="search__filename">{baseName(path)}</span>
                <span className="search__filedir">{dirName(path)}</span>
                <span className="search__count">{matches.length}</span>
              </button>
              {!isCollapsed &&
                matches.map((m, i) => (
                  <button
                    type="button"
                    key={`${m.line}:${m.column}:${i}`}
                    className="search__match"
                    onClick={() => void openFileAt(m.path, m.line, m.column)}
                    title={`${m.path}:${m.line}`}
                    aria-label={`Open ${baseName(m.path)}, line ${m.line}: ${m.text.trim()}`}
                  >
                    <span className="search__line">{m.line}</span>
                    <span className="search__text">{m.text.trim()}</span>
                  </button>
                ))}
            </div>
          );
        })}
      </div>
    </div>
  );
}
