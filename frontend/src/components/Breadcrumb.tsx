import { ChevronRight } from "lucide-react";
import { useStore } from "../state/store";

export default function Breadcrumb() {
  const tab = useStore((s) => s.tabs.find((t) => t.path === s.activePath) ?? null);
  if (!tab) return null;

  let path = tab.path;
  if (tab.kind === "diff" || tab.kind === "table") path = tab.rel ?? tab.path;
  else if (tab.kind === "db") path = tab.name;

  // Normalise Windows backslashes so a backslash-separated path doesn't render
  // as one giant segment.
  const segs = path.replace(/\\/g, "/").split("/").filter(Boolean);
  if (segs.length === 0) return null;

  return (
    <div className="breadcrumb">
      {segs.map((seg, i) => (
        <span key={i} className="breadcrumb__seg">
          {i > 0 && <ChevronRight size={12} className="breadcrumb__sep" />}
          {seg}
        </span>
      ))}
    </div>
  );
}
