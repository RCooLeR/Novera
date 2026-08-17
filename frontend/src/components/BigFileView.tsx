import { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowDownToLine, Binary, FileText, Pencil, Search as SearchIcon } from "lucide-react";
import { BigFile, errMessage } from "../lib/services";
import type { StagingState } from "../lib/services";
import { highlighterFor } from "../lib/lineHighlight";
import type { LineTokenizer, Seg } from "../lib/lineHighlight";
import { SerialTaskQueue } from "../lib/serialTaskQueue";
import { LatestRequest } from "../lib/latestRequest";
import { BigFileSearchRequestOwner } from "../lib/bigFileSearchRequest";
import {
  prepareBigFileEditEntry,
  releaseBigFileEditIfClean,
} from "../lib/bigFileEditLifecycle";
import {
  bigFileLineTarget,
  exactSearchLocation,
  normalizeBigFileHexWindow,
  normalizeBigFileMatchWindow,
  normalizeBigFileState,
  normalizeBigFileTextWindow,
  parseBigFileLocation,
  requireAdjacentPreviousTextWindow,
} from "../lib/bigFileNavigation";
import type { BigFileTextWindow } from "../lib/bigFileNavigation";
import { useStore } from "../state/store";
import LargeFileSaveActions from "./LargeFileSaveActions";

const editEnc = new TextEncoder();
const byteLen = (s: string) => editEnc.encode(s).length; // UTF-8 byte length (StageEdit origLen)
const EDIT_WINDOW_BYTES = 256 * 1024; // editable window budget (textarea-friendly)

/* ---------------------------------------------------------------------------
 * BigFileView — windowed viewer for files of ANY size, backed by the ported
 * Novera's large-file streaming engine (internal/bigfile). The whole file never enters
 * memory: the backend serves bounded, line-aligned windows; this component
 * keeps a bounded ring of rows in the DOM and pages new windows in/out as the
 * user scrolls. Adds true line numbers, in-file search, go-to, a hex mode,
 * follow-tail, and windowed editing (UTF-8/LF files) with copy-only saving —
 * the staging + save machinery lives in the Go engine.
 * ------------------------------------------------------------------------- */

const ROW_H = 18; // px — must match .bfv__row height in global.css
const WINDOW_BYTES = 1 << 20; // 1 MiB text window budget
const HEX_BYTES = 64 * 1024; // hex window budget
const MAX_ROWS = 24000; // bounded DOM ring; trims the far edge past this
const EDGE_PX = 700; // distance from an edge that triggers a page load
const PREV_LOOKBACK = 128 * 1024;

type Mode = "text" | "hex";
type Row = { gutter: string; off: number; text: string; segs?: Seg[]; matchFrom?: number; matchTo?: number };
type Loaded = { rows: Row[]; nextByte: number; atBof: boolean; atEof: boolean; approx: boolean };

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

export default function BigFileView({
	abs,
	name,
	tabPath,
	binaryHint,
	sourceVersion = 0,
	staleOnDisk = false,
}: {
	abs: string;
	name: string;
	tabPath: string;
	binaryHint?: boolean;
	sourceVersion?: number;
	staleOnDisk?: boolean;
}) {
  const [fileId, setFileId] = useState("");
  const [size, setSize] = useState(0);
  const [encoding, setEncoding] = useState("");
  const [binary, setBinary] = useState(!!binaryHint);
  const [mode, setMode] = useState<Mode>(binaryHint ? "hex" : "text");
  const [rows, setRows] = useState<Row[]>([]);
  const [atBof, setAtBof] = useState(true);
  const [atEof, setAtEof] = useState(false);
  const [approx, setApprox] = useState(false);
  const [error, setError] = useState("");
  const [follow, setFollow] = useState(false);

  // Search / go-to / status.
  const [query, setQuery] = useState("");
  const [regex, setRegex] = useState(false);
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [gotoVal, setGotoVal] = useState("");
  const [note, setNote] = useState("");
  const [highlightOff, setHighlightOff] = useState<number | null>(null);

  // Editing (UTF-8/LF files only). One window is editable at a time; staged
  // edits accumulate in the engine session and are applied on save.
  const [editable, setEditable] = useState(false);
  const [editMode, setEditMode] = useState(false);
  const [editText, setEditText] = useState("");
  const [editStart, setEditStart] = useState(0);
  const [staging, setStaging] = useState<StagingState | null>(null);
	const [saving, setSaving] = useState(false);
	const hasStagedEdits = (staging?.editCount ?? 0) > 0;
	const setLargeFileState = useStore((state) => state.setLargeFileState);
	const reloadTab = useStore((state) => state.reloadTab);
	const closeRequested = useStore((state) => state.pendingTabClose === tabPath);

  const scrollRef = useRef<HTMLDivElement | null>(null);
  const tailNextRef = useRef(0); // byte to load forward from
  const loadingRef = useRef(false);
  const loadingGenerationRef = useRef(0);
  const cursorRef = useRef(0); // search cursor (byte offset) — first search only
  const lastHitRef = useRef<{ start: number; end: number } | null>(null); // last match, for direction-aware stepping
  const modeRef = useRef<Mode>(mode); // current mode, read inside the follow-tail closure
  modeRef.current = mode;
  const tokenizerRef = useRef<LineTokenizer | null>(null); // per-line syntax highlighter
  const editOrigLenRef = useRef(0); // byte length of the currently-staged window text
  const editTextRef = useRef(""); // latest textarea value (for debounced staging)
  const stagingRef = useRef<StagingState | null>(null);
  const stageTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const stageQueueRef = useRef(new SerialTaskQueue());
  const viewRequestsRef = useRef(new LatestRequest());
  const findContextRequestsRef = useRef(new LatestRequest());
  const gotoContextRequestsRef = useRef(new LatestRequest());
  const searchRequestOwnerRef = useRef(
    new BigFileSearchRequestOwner((requestId) => {
      void BigFile.CancelSearch(requestId).catch(() => undefined);
    }),
  );
  const sourceVersionRef = useRef(sourceVersion);
  // Deferred scroll op applied after rows commit: keep position stable on
  // prepend/trim, or snap to top/bottom on reset.
  const scrollOpRef = useRef<{ kind: "delta" | "top" | "bottom"; value: number } | null>(null);

  const fid = fileId; // stable alias for callbacks

  const cancelActiveSearch = useCallback(() => {
    searchRequestOwnerRef.current.cancel();
  }, []);

  const clearFindContext = useCallback(() => {
    cancelActiveSearch();
    findContextRequestsRef.current.invalidate();
    lastHitRef.current = null;
    setHighlightOff(null);
    setNote("");
  }, [cancelActiveSearch]);

  // ---- window → rows ----
  // Tokenise each line once, at load time (not per render), so syntax colours
  // are computed a window at a time and reused as the user scrolls.
  const textRows = (w: { text: string; lineNumbers: number[]; lineOffsets: number[] }): Row[] => {
    const lines = w.lineOffsets.length ? w.text.split("\n") : [];
    const tok = tokenizerRef.current;
    return lines.map((t, i) => ({
      gutter: String(w.lineNumbers[i] ?? ""),
      off: Number(w.lineOffsets[i] ?? 0),
      text: t,
      segs: tok ? tok(t) : undefined,
    }));
  };

  const loadText = useCallback(async (id: string, startByte: number): Promise<Loaded> => {
    const w = normalizeBigFileTextWindow(await BigFile.GetWindow(id, startByte, WINDOW_BYTES), id);
    return { rows: textRows(w), nextByte: w.nextByte, atBof: w.atBof, atEof: w.atEof, approx: w.approx };
  }, []);

  const loadTextTail = useCallback(async (id: string): Promise<Loaded> => {
    const w = normalizeBigFileTextWindow(await BigFile.GetTailWindow(id, WINDOW_BYTES), id);
    return { rows: textRows(w), nextByte: w.nextByte, atBof: w.atBof, atEof: w.atEof, approx: w.approx };
  }, []);

  const loadTextPrev = useCallback(async (id: string, currentStart: number): Promise<Loaded> => {
    const first = requireAdjacentPreviousTextWindow(
      normalizeBigFileTextWindow(
        await BigFile.GetPrevWindow(id, currentStart, PREV_LOOKBACK),
        id,
      ),
      currentStart,
    );
    return {
      rows: textRows(first).filter((r) => r.off < currentStart),
      nextByte: first.nextByte,
      atBof: first.atBof,
      atEof: false,
      approx: first.approx,
    };
  }, []);

  const loadHex = useCallback(async (id: string, startByte: number): Promise<Loaded> => {
    const h = normalizeBigFileHexWindow(
      await BigFile.GetHexWindow(id, startByte, HEX_BYTES),
      id,
    );
    const out = h.lines.map((l) => ({
      gutter: Number(l.offset).toString(16).padStart(8, "0"),
      off: Number(l.offset),
      text: l.hex.padEnd(48, " ") + " |" + l.ascii + "|",
    }));
    return { rows: out, nextByte: h.nextByte, atBof: h.atBof, atEof: h.atEof, approx: false };
  }, []);

  const loadHexPrev = useCallback(
    async (id: string, currentStart: number): Promise<Loaded> => {
      let start = Math.max(0, currentStart - HEX_BYTES);
      start -= start % 16;
      const h = normalizeBigFileHexWindow(
        await BigFile.GetHexWindow(id, start, HEX_BYTES),
        id,
      );
      const out = h.lines
        .filter((l) => Number(l.offset) < currentStart)
        .map((l) => ({
          gutter: Number(l.offset).toString(16).padStart(8, "0"),
          off: Number(l.offset),
          text: l.hex.padEnd(48, " ") + " |" + l.ascii + "|",
        }));
      return { rows: out, nextByte: currentStart, atBof: start <= 0, atEof: false, approx: false };
    },
    [],
  );

  const loadAt = useCallback(
    (id: string, startByte: number) => (mode === "hex" ? loadHex(id, startByte) : loadText(id, startByte)),
    [mode, loadHex, loadText],
  );
  const loadPrevAt = useCallback(
    (id: string, anchor: number) => (mode === "hex" ? loadHexPrev(id, anchor) : loadTextPrev(id, anchor)),
    [mode, loadHexPrev, loadTextPrev],
  );

  // Reset the buffer to a fresh window at startByte. scrollTo snaps the viewport.
  const reset = useCallback(
    async (id: string, startByte: number, scrollTo: "top" | "bottom" = "top", highlight: number | null = null) => {
      const generation = viewRequestsRef.current.begin();
      loadingGenerationRef.current = generation;
      loadingRef.current = true;
      try {
        const r = await loadAt(id, startByte);
        if (!viewRequestsRef.current.isCurrent(generation)) return;
        tailNextRef.current = r.nextByte;
        setAtBof(r.atBof);
        setAtEof(r.atEof);
        setApprox(r.approx);
        setHighlightOff(highlight);
        scrollOpRef.current = { kind: scrollTo, value: 0 };
        setRows(r.rows);
      } catch (resetError) {
        if (viewRequestsRef.current.isCurrent(generation)) {
          setNote(errMessage(resetError));
        }
      } finally {
        if (loadingGenerationRef.current === generation) loadingRef.current = false;
      }
    },
    [loadAt],
  );

  // Watcher-driven source changes keep this long-lived component/session
  // mounted. Refresh the backend document explicitly, but never refresh across
  // staged edits: the store marks that case stale until the user resolves it.
  useEffect(() => {
    if (sourceVersionRef.current === sourceVersion) return;
    sourceVersionRef.current = sourceVersion;
    if (!fid || staleOnDisk || hasStagedEdits) return;
    const generation = viewRequestsRef.current.begin();
    setFollow(false);
    void (async () => {
      try {
        const refreshed = await BigFile.RefreshFile(fid);
        const total = Number(refreshed.size);
        if (!Number.isSafeInteger(total) || total < 0) {
          throw new Error("Large-file refresh returned an inexact file size.");
        }
        if (!viewRequestsRef.current.isCurrent(generation)) return;
        setSize(total);
        setNote("Reloaded after an external file change.");
        await reset(fid, 0, "top", null);
      } catch (refreshError) {
        if (viewRequestsRef.current.isCurrent(generation)) setNote(errMessage(refreshError));
      }
    })();
  }, [fid, hasStagedEdits, reset, sourceVersion, staleOnDisk]);

  useEffect(() => {
    if (!staleOnDisk) return;
    setFollow(false);
    setNote("This file changed on disk. Save a copy or discard staged edits before reloading.");
  }, [staleOnDisk]);

  // ---- open / close the file in the engine ----
  useEffect(() => {
    let alive = true;
    let openedId = "";
    const generation = viewRequestsRef.current.begin();
    loadingGenerationRef.current = generation;
    setError("");
    setRows([]);
		BigFile.OpenFile(abs)
      .then((m) => {
        openedId = m.fileId;
        if (!alive || !viewRequestsRef.current.isCurrent(generation)) {
          void BigFile.CloseFile(m.fileId);
          return;
        }
        setFileId(m.fileId);
        setSize(Number(m.size));
        setEncoding(m.encoding || "binary");
        setBinary(m.binary);
        setEditable(m.editable);
        setEditMode(false);
        stagingRef.current = null;
        setStaging(null);
        tokenizerRef.current = m.binary ? null : highlighterFor(m.detected, name);
        const initialMode: Mode = m.binary ? "hex" : "text";
        setMode(initialMode);
        loadingRef.current = true;
        const first = initialMode === "hex" ? loadHex(m.fileId, 0) : loadText(m.fileId, 0);
        return first.then((r) => {
          if (!alive || !viewRequestsRef.current.isCurrent(generation)) return;
          tailNextRef.current = r.nextByte;
          setAtBof(r.atBof);
          setAtEof(r.atEof);
          setApprox(r.approx);
          scrollOpRef.current = { kind: "top", value: 0 };
          setRows(r.rows);
          if (loadingGenerationRef.current === generation) loadingRef.current = false;
        });
      })
      .catch((e) => {
        if (alive && viewRequestsRef.current.isCurrent(generation)) setError(errMessage(e));
      })
      .finally(() => {
        if (alive && loadingGenerationRef.current === generation) loadingRef.current = false;
      });
		return () => {
			alive = false;
      cancelActiveSearch();
      viewRequestsRef.current.invalidate();
      findContextRequestsRef.current.invalidate();
      gotoContextRequestsRef.current.invalidate();
			setLargeFileState(tabPath, { sessionId: "" });
			if (openedId) {
        if (stagingRef.current) {
          void BigFile.ReleaseCleanEditSession(openedId)
            .then(() => BigFile.CloseFile(openedId))
            .catch(() => undefined);
        } else {
          void BigFile.CloseFile(openedId);
        }
      }
		};
	}, [abs, cancelActiveSearch, loadHex, loadText, setLargeFileState, tabPath]);

	useEffect(() => {
		setLargeFileState(tabPath, {
			sessionId: fid,
			dirty: hasStagedEdits,
		});
	}, [fid, hasStagedEdits, setLargeFileState, tabPath]);

  // Apply the deferred scroll op once rows have committed (fixed row height
  // makes prepend/trim compensation exact).
  useLayoutEffect(() => {
    const el = scrollRef.current;
    const op = scrollOpRef.current;
    if (!el || !op) return;
    scrollOpRef.current = null;
    if (op.kind === "top") el.scrollTop = 0;
    else if (op.kind === "bottom") el.scrollTop = el.scrollHeight;
    else el.scrollTop += op.value;
  }, [rows]);

  const appendNext = useCallback(async () => {
    if (loadingRef.current || atEof || !fid) return;
    cancelActiveSearch();
    const generation = viewRequestsRef.current.begin();
    loadingGenerationRef.current = generation;
    loadingRef.current = true;
    try {
      const r = await loadAt(fid, tailNextRef.current);
      if (!viewRequestsRef.current.isCurrent(generation)) return;
      if (r.rows.length === 0) {
        setAtEof(true);
        return;
      }
      tailNextRef.current = r.nextByte;
      setApprox(r.approx);
      setAtEof(r.atEof);
      setRows((prev) => {
        let next = [...prev, ...r.rows];
        if (next.length > MAX_ROWS) {
          const trim = next.length - MAX_ROWS;
          next = next.slice(trim);
          scrollOpRef.current = { kind: "delta", value: -trim * ROW_H };
          setAtBof(false);
        }
        return next;
      });
    } catch (e) {
      if (viewRequestsRef.current.isCurrent(generation)) setNote(errMessage(e));
    } finally {
      if (loadingGenerationRef.current === generation) loadingRef.current = false;
    }
  }, [atEof, cancelActiveSearch, fid, loadAt]);

  const prependPrev = useCallback(async () => {
    if (loadingRef.current || atBof || !fid || rows.length === 0) return;
    cancelActiveSearch();
    const generation = viewRequestsRef.current.begin();
    loadingGenerationRef.current = generation;
    loadingRef.current = true;
    try {
      const anchor = rows[0].off;
      const r = await loadPrevAt(fid, anchor);
      if (!viewRequestsRef.current.isCurrent(generation)) return;
      if (r.rows.length === 0) {
        setAtBof(true);
        return;
      }
      setAtBof(r.atBof);
      setApprox(r.approx);
      setRows((prev) => {
        let next = [...r.rows, ...prev];
        const adjust = r.rows.length * ROW_H;
        if (next.length > MAX_ROWS) {
          const trim = next.length - MAX_ROWS;
          tailNextRef.current = next[next.length - trim].off; // new tail anchor
          next = next.slice(0, next.length - trim);
          setAtEof(false);
        }
        scrollOpRef.current = { kind: "delta", value: adjust };
        return next;
      });
    } catch (e) {
      if (viewRequestsRef.current.isCurrent(generation)) setNote(errMessage(e));
    } finally {
      if (loadingGenerationRef.current === generation) loadingRef.current = false;
    }
  }, [atBof, cancelActiveSearch, fid, rows, loadPrevAt]);

  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el || loadingRef.current) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - EDGE_PX) void appendNext();
    else if (el.scrollTop <= EDGE_PX) void prependPrev();
  }, [appendNext, prependPrev]);

  // ---- search ----
  const runFind = useCallback(
    async (backward: boolean) => {
      if (!fid || !query.trim()) return;
      cancelActiveSearch();
      const generation = viewRequestsRef.current.begin();
      const contextGeneration = findContextRequestsRef.current.begin();
      const isCurrent = () =>
        viewRequestsRef.current.isCurrent(generation) &&
        findContextRequestsRef.current.isCurrent(contextGeneration);
      let searchSettled = false;
      let requestId = "";
      setFollow(false);
      setNote("Searching…");
      try {
        requestId = await BigFile.BeginSearchRequest(fid);
        if (!isCurrent()) {
          void BigFile.CancelSearch(requestId).catch(() => undefined);
          requestId = "";
          return;
        }
        searchRequestOwnerRef.current.activate(requestId);
        // Seed from the last hit's edge so a direction flip steps to the
        // adjacent match instead of re-finding the current one (next is
        // at/after fromByte; previous is strictly before beforeByte).
        const last = lastHitRef.current;
        const from = backward ? (last ? last.start : cursorRef.current) : last ? last.end : cursorRef.current;
        const hit = backward
          ? await BigFile.FindPrevRequest(requestId, query, from, regex, caseSensitive, false)
          : await BigFile.FindNextRequest(requestId, query, from, regex, caseSensitive, false);
        searchRequestOwnerRef.current.settle(requestId);
        if (!isCurrent()) return;
        if (hit.unsupported) {
          setNote(hit.message || "Search not supported for this file.");
          searchSettled = true;
          return;
        }
        if (hit.timedOut) {
          setNote("Search timed out.");
          searchSettled = true;
          return;
        }
        if (!hit.found) {
          setNote("No matches.");
          searchSettled = true;
          return;
        }
        const exact = exactSearchLocation(hit.offset, hit.length, hit.line);
        if (!exact.ok) {
          setNote(exact.message);
          searchSettled = true;
          return;
        }
        const { offset: off, length, line } = exact.location;
        lastHitRef.current = { start: off, end: off + Math.max(1, length) };
        cursorRef.current = off;
        setNote(`Line ${line.toLocaleString()}`);
        searchSettled = true;
        if (mode === "hex") await reset(fid, off - (off % 16), "top", off);
        else {
          const match = normalizeBigFileMatchWindow(
            await BigFile.GetMatchWindow(fid, off, length, WINDOW_BYTES),
            fid,
          );
          if (!isCurrent()) return;
          if (!match.found) {
            await reset(fid, off, "top", off);
          } else {
            const matchedRows = textRows(match.window);
            let textStart = 0;
            for (const row of matchedRows) {
              const textEnd = textStart + row.text.length;
              if (match.from >= textStart && match.to <= textEnd) {
                row.matchFrom = match.from - textStart;
                row.matchTo = match.to - textStart;
                break;
              }
              textStart = textEnd + 1;
            }
            tailNextRef.current = match.window.nextByte;
            setAtBof(match.window.atBof);
            setAtEof(match.window.atEof);
            setApprox(match.window.approx);
            setHighlightOff(off);
            scrollOpRef.current = { kind: "top", value: 0 };
            setRows(matchedRows);
          }
        }
      } catch (e) {
        if (isCurrent()) {
          setNote(errMessage(e));
          searchSettled = true;
        }
      } finally {
        if (requestId) searchRequestOwnerRef.current.settle(requestId);
        // Scrolling, reloading, or switching view can supersede the lookup
        // without starting another search. Do not leave its transient status
        // visible forever, but never clear a newer query's status.
        if (!searchSettled && findContextRequestsRef.current.isCurrent(contextGeneration)) {
          setNote((current) => (current === "Searching…" ? "" : current));
        }
      }
    },
    [cancelActiveSearch, fid, query, regex, caseSensitive, mode, reset],
  );

  // ---- go to line / 0xOFFSET / percent ----
  const runGoto = useCallback(async () => {
    if (!fid) return;
    cancelActiveSearch();
    const location = parseBigFileLocation(gotoVal, size);
    if (location.kind === "error") {
      setNote(location.message);
      return;
    }
    const generation = viewRequestsRef.current.begin();
    const contextGeneration = gotoContextRequestsRef.current.begin();
    const isCurrent = () =>
      viewRequestsRef.current.isCurrent(generation) &&
      gotoContextRequestsRef.current.isCurrent(contextGeneration);
    setFollow(false);
    try {
      let byte: number;
      let gotoMessage = "";
      if (location.kind === "byte") {
        byte = location.offset;
      } else {
        const target = bigFileLineTarget(await BigFile.ResolveLine(fid, location.line), location.line);
        if (!isCurrent()) return;
        if (target.kind === "unavailable") {
          setNote(target.message);
          return;
        }
        byte = target.offset;
        gotoMessage = target.message;
      }
      if (!isCurrent()) return;
      if (mode === "hex") byte -= byte % 16;
      cursorRef.current = Math.max(0, byte); // search continues from where we jumped
      lastHitRef.current = null;
      setNote(gotoMessage);
      await reset(fid, Math.max(0, byte), "top", null);
    } catch (e) {
      if (isCurrent()) setNote(errMessage(e));
    }
  }, [cancelActiveSearch, fid, gotoVal, size, mode, reset]);

  const switchMode = useCallback(
    async (m: Mode) => {
      if (m === mode || !fid) return;
      cancelActiveSearch();
      const anchor = rows[0]?.off ?? 0;
      const generation = viewRequestsRef.current.begin();
      loadingGenerationRef.current = generation;
      loadingRef.current = true;
      setFollow(false);
      setMode(m);
      try {
        const r = m === "hex" ? await loadHex(fid, anchor - (anchor % 16)) : await loadText(fid, anchor);
        if (!viewRequestsRef.current.isCurrent(generation)) return;
        tailNextRef.current = r.nextByte;
        setAtBof(r.atBof);
        setAtEof(r.atEof);
        setApprox(r.approx);
        setHighlightOff(null);
        scrollOpRef.current = { kind: "top", value: 0 };
        setRows(r.rows);
      } catch (error) {
        if (viewRequestsRef.current.isCurrent(generation)) {
          setMode(mode);
          setNote(errMessage(error));
        }
      } finally {
        if (loadingGenerationRef.current === generation) loadingRef.current = false;
      }
    },
    [cancelActiveSearch, mode, fid, rows, loadHex, loadText],
  );

  // ---- editing ----
  const fetchEditWindow = useCallback(
    async (start: number) => {
      if (!fid) throw new Error("Large-file edit session is unavailable.");
      return normalizeBigFileTextWindow(
        await BigFile.GetEditWindow(fid, start, EDIT_WINDOW_BYTES),
        fid,
        EDIT_WINDOW_BYTES + 1,
      );
    },
    [fid],
  );

  const applyEditWindow = useCallback((w: BigFileTextWindow) => {
      setEditStart(w.startByte);
      editOrigLenRef.current = w.nextByte - w.startByte;
      editTextRef.current = w.text;
      setEditText(w.text);
  }, []);

  // Stage the current textarea against the window it was loaded from. After
  // staging, the edited view of [editStart, editStart+byteLen(text)) equals the
  // text, so origLen for the next stage becomes that byte length.
	const stageNow = useCallback((): Promise<boolean> => {
		if (!fid) return Promise.resolve(false);
		const txt = editTextRef.current;
		const start = editStart;
		let outcome = false;
		return stageQueueRef.current.run(async () => {
			try {
				// Read origLen only when this mutation reaches the head of the queue.
				// A preceding stage may have changed the edited window's byte length.
				const st = await BigFile.StageEdit(fid, start, editOrigLenRef.current, txt);
				editOrigLenRef.current = byteLen(txt);
        stagingRef.current = st;
				setStaging(st);
				setLargeFileState(tabPath, { dirty: st.editCount > 0 });
				outcome = true;
			} catch (error) {
				setNote(errMessage(error));
			}
			return outcome;
		});
	}, [editStart, fid, setLargeFileState, tabPath]);

	const onEditChange = useCallback(
		(v: string) => {
			setEditText(v);
			editTextRef.current = v;
			setLargeFileState(tabPath, { dirty: true });
			clearTimeout(stageTimerRef.current);
			stageTimerRef.current = setTimeout(() => void stageNow(), 700);
		},
		[setLargeFileState, stageNow, tabPath],
	);

  const enterEdit = useCallback(async () => {
    if (!editable || !fid) return;
    cancelActiveSearch();
    setFollow(false);
    setSaving(true);
    try {
      const target = editMode ? editStart : (rows[0]?.off ?? 0);
      await prepareBigFileEditEntry(
        () => BigFile.PrepareEditSession(fid),
        () => fetchEditWindow(target),
        (prepared, window) => {
          stagingRef.current = prepared;
          setStaging(prepared);
          applyEditWindow(window);
          setEditMode(true);
        },
        () => BigFile.ReleaseCleanEditSession(fid),
      );
    } catch (error) {
      setNote(errMessage(error));
    } finally {
      setSaving(false);
    }
  }, [applyEditWindow, cancelActiveSearch, editable, fid, editMode, editStart, fetchEditWindow, rows]);

  const exitEdit = useCallback(async () => {
    if (!fid) return;
    clearTimeout(stageTimerRef.current);
		setSaving(true);
		try {
      if (!(await stageNow())) return;
      const released = await releaseBigFileEditIfClean(
        stagingRef.current,
        () => BigFile.ReleaseCleanEditSession(fid),
      );
      if (released !== null) {
        stagingRef.current = released;
        setStaging(released);
      }
      setEditMode(false);
    } catch (error) {
      setNote(errMessage(error));
		} finally {
			setSaving(false);
		}
  }, [fid, stageNow]);

  const doDiscard = useCallback(async () => {
    if (!fid) return;
    clearTimeout(stageTimerRef.current);
		setSaving(true);
    try {
			await stageQueueRef.current.drain();
      const st = await BigFile.DiscardEdits(fid);
      stagingRef.current = st;
      setStaging(st);
			setLargeFileState(tabPath, { dirty: false });
      const anchor = editMode ? editStart : (rows[0]?.off ?? 0);
      setEditMode(false);
      await reset(fid, anchor, "top", null);
      setNote("Discarded staged edits.");
    } catch (e) {
      setNote(errMessage(e));
		} finally {
			setSaving(false);
    }
	}, [fid, editMode, editStart, reset, rows, setLargeFileState, tabPath]);

  const doSaveCopy = useCallback(async () => {
    if (!fid) return;
    clearTimeout(stageTimerRef.current);
    setSaving(true);
    try {
			if (!(await stageNow())) return;
      const r = await BigFile.SaveCopyViaDialog(fid);
      if (r.mode) setNote(`Saved edited copy → ${r.outputPath}`);
    } catch (e) {
      setNote(errMessage(e));
    } finally {
      setSaving(false);
    }
  }, [fid, stageNow]);

	useEffect(() => () => clearTimeout(stageTimerRef.current), []);
	useEffect(() => {
		if (closeRequested) clearTimeout(stageTimerRef.current);
	}, [closeRequested]);

	// Following performs a document refresh when the file grows. A staged edit
	// owns the current document generation, so stop follow immediately and make
	// the user explicitly save or discard before live refresh can resume.
	useEffect(() => {
		if (hasStagedEdits) setFollow(false);
	}, [hasStagedEdits]);

  // ---- follow-tail ----
  useEffect(() => {
		if (!follow || !fid || hasStagedEdits) return;
    let cancelled = false;
    let polling = false;
    cancelActiveSearch();

    // Text tailing is one backend operation that walks backward from EOF under
    // a strict byte/line budget. Hex mode is already byte-exact, so one final
    // fixed-size hex window is sufficient as well.
    const jumpTail = async (total: number, generation: number) => {
      const hex = modeRef.current === "hex";
      const r = hex
        ? await loadHex(fid, Math.ceil(Math.max(0, total - HEX_BYTES) / 16) * 16)
        : await loadTextTail(fid);
      if (cancelled || !viewRequestsRef.current.isCurrent(generation)) return;
      tailNextRef.current = r.nextByte;
      setAtBof(r.atBof);
      setAtEof(r.atEof);
      setApprox(r.approx);
      scrollOpRef.current = { kind: "bottom", value: 0 };
      setRows(r.rows.length > MAX_ROWS ? r.rows.slice(-MAX_ROWS) : r.rows);
    };

    const poll = async (initial: boolean) => {
      if (polling || cancelled) return;
      polling = true;
      try {
        const state = normalizeBigFileState(await BigFile.FileState(fid));
        const changed = !state.sameOpenedFile || state.changedFromOpen;
        if (!initial && !changed) return;
        const generation = viewRequestsRef.current.begin();
        let total = state.size;
        if (changed) {
          const refreshed = await BigFile.RefreshFile(fid);
          total = Number(refreshed.size);
        }
        if (cancelled || !viewRequestsRef.current.isCurrent(generation)) return;
        setSize(total);
        await jumpTail(total, generation);
      } catch {
        /* ignore */
      } finally {
        polling = false;
      }
    };

    void poll(true);
    const t = setInterval(() => {
      void poll(false);
    }, 1200);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
	}, [cancelActiveSearch, follow, fid, hasStagedEdits, loadHex, loadTextTail]);

  if (error) return <div className="editor-placeholder" role="alert">{error}</div>;

  return (
    <div className="bfv">
      <div className="bfv__bar">
        <span className="bfv__name">{name}</span>
        <span className="bfv__badge">
          {binary ? "binary" : encoding || "text"}
          {!binary && !editable ? " · read-only" : ""}
        </span>
        <span className="bfv__size">{fmtBytes(size)}</span>
        {approx && mode === "text" && (
          <span className="bfv__approx" title="Line numbers are approximate until background indexing finishes">
            ≈ lines
          </span>
        )}
        <span className="bfv__spacer" />

        {!editMode && !binary && (
          <div className="bfv__search">
            <SearchIcon size={13} className="bfv__searchicon" aria-hidden="true" />
            <input
              className="bfv__input"
              aria-label="Find in large file"
              placeholder="Find…"
              value={query}
              onChange={(e) => {
                clearFindContext();
                setQuery(e.target.value);
                lastHitRef.current = null; // new query → search from the current anchor again
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") void runFind(e.shiftKey);
              }}
            />
            <button
              type="button"
              className={`bfv__opt${regex ? " on" : ""}`}
              aria-label="Use regular expression"
              aria-pressed={regex}
              title="Regex"
              onClick={() => {
                clearFindContext();
                setRegex((value) => !value);
              }}
            >
              .*
            </button>
            <button
              type="button"
              className={`bfv__opt${caseSensitive ? " on" : ""}`}
              aria-label="Match case"
              aria-pressed={caseSensitive}
              title="Match case"
              onClick={() => {
                clearFindContext();
                setCaseSensitive((value) => !value);
              }}
            >
              Aa
            </button>
            <button type="button" className="bfv__btn" aria-label="Find previous match" title="Find previous" onClick={() => void runFind(true)}>
              ↑
            </button>
            <button type="button" className="bfv__btn" aria-label="Find next match" title="Find next" onClick={() => void runFind(false)}>
              ↓
            </button>
          </div>
        )}

        {!editMode && (
          <input
            className="bfv__input bfv__goto"
            aria-label="Go to line, hexadecimal byte offset, or percentage"
            placeholder="line / 0x / %"
            value={gotoVal}
            onChange={(e) => {
              gotoContextRequestsRef.current.invalidate();
              setGotoVal(e.target.value);
              setNote("");
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") void runGoto();
            }}
            title="Go to line number, 0xHEX offset, or percent"
          />
        )}

        {!editMode && !binaryHint && (
          <button
            type="button"
            className={`bfv__toggle${mode === "hex" ? " on" : ""}`}
            aria-label={mode === "hex" ? "Switch to text view" : "Switch to hex view"}
            aria-pressed={mode === "hex"}
            title="Toggle hex view"
            onClick={() => void switchMode(mode === "hex" ? "text" : "hex")}
          >
            {mode === "hex" ? <FileText size={14} /> : <Binary size={14} />}
          </button>
        )}
        {!editMode && (
					<button
            type="button"
						className={`bfv__toggle${follow ? " on" : ""}`}
            aria-label="Follow file tail"
            aria-pressed={follow}
						disabled={hasStagedEdits}
						title={hasStagedEdits ? "Save or discard staged edits before following the live file" : "Follow tail (live)"}
						onClick={() => setFollow((v) => !v)}
					>
            <ArrowDownToLine size={14} />
          </button>
        )}
        {!binary && (
          <button
            type="button"
            className={`bfv__toggle${editMode ? " on" : ""}`}
            aria-label={editMode ? "Finish editing large file" : "Edit large file"}
            aria-pressed={editMode}
            disabled={!editable || saving}
            title={editable ? "Edit file" : `Editing needs a UTF-8 / LF file (this is ${encoding || "?"})`}
            onClick={() => (editMode ? void exitEdit() : void enterEdit())}
          >
            <Pencil size={14} />
          </button>
        )}
      </div>

      {note && <div className="bfv__note" role="status" aria-live="polite">{note}</div>}

      {staleOnDisk && (
        <div className="bfv__editbanner" role="alert">
          <span>The source changed on disk. Save a copy or discard staged edits before reloading.</span>
          <span className="bfv__spacer" />
          <button
            type="button"
            className="btn"
            disabled={saving || hasStagedEdits}
            title={hasStagedEdits ? "Save a copy or discard staged edits before reloading" : "Reload the latest disk version"}
            onClick={() => void reloadTab(tabPath)}
          >
            Reload from disk
          </button>
        </div>
      )}

      {!editMode && staging && staging.editCount > 0 && (
        <div className="bfv__editbanner">
          <span>
            {staging.editCount} unsaved edit{staging.editCount === 1 ? "" : "s"}
            {staging.netDelta !== 0
              ? ` · ${staging.netDelta > 0 ? "+" : ""}${Number(staging.netDelta).toLocaleString()} bytes`
              : ""}
          </span>
          <span className="bfv__spacer" />
          <button type="button" className="btn" onClick={enterEdit}>
            Resume
          </button>
          <LargeFileSaveActions
            saving={saving}
            hasEdits
            discardDanger
            onSaveCopy={() => void doSaveCopy()}
            onDiscard={() => void doDiscard()}
          />
        </div>
      )}

      {editMode ? (
        <div className="bfv__editwrap">
          <div className="bfv__edittoolbar">
            <span>
              Editing · {staging?.editCount ?? 0} edit{(staging?.editCount ?? 0) === 1 ? "" : "s"}
              {staging && staging.netDelta !== 0
                ? ` · ${staging.netDelta > 0 ? "+" : ""}${Number(staging.netDelta).toLocaleString()} B`
                : ""}
            </span>
            <span className="bfv__spacer" />
            <LargeFileSaveActions
              saving={saving}
              hasEdits={!!staging?.editCount}
              onSaveCopy={() => void doSaveCopy()}
              onDiscard={() => void doDiscard()}
            />
            <button type="button" className="btn btn--primary" disabled={saving} onClick={() => void exitEdit()}>
              Done
            </button>
          </div>
          <textarea
            className="bfv__edit"
            aria-label={`Edit window for ${name}`}
            value={editText}
				disabled={saving}
            spellCheck={false}
            wrap="off"
            onChange={(e) => onEditChange(e.target.value)}
          />
        </div>
      ) : (
        <div className="bfv__body" ref={scrollRef} onScroll={onScroll} role="region" aria-label={`${name} large-file contents`}>
          {!atBof && <div className="bfv__edge">↑ more above</div>}
          {rows.map((r, i) => {
            const next = rows[i + 1]?.off ?? Infinity;
            const hit = highlightOff != null && r.off <= highlightOff && highlightOff < next;
            const exactMatch = r.matchFrom !== undefined && r.matchTo !== undefined;
            return (
              <div className={`bfv__row${hit ? " hit" : ""}`} key={`${r.off}-${i}`}>
                <span className="bfv__gutter">{r.gutter}</span>
                <span className="bfv__text">
                  {exactMatch ? (
                    <>
                      {r.text.slice(0, r.matchFrom)}
                      <span className="bfv__match">
                        {r.text.slice(r.matchFrom, r.matchTo) || "\u200b"}
                      </span>
                      {r.text.slice(r.matchTo)}
                    </>
                  ) : r.segs ? (
                    r.segs.map((s, j) =>
                      s.c ? <span key={j} className={s.c}>{s.t}</span> : <Fragment key={j}>{s.t}</Fragment>,
                    )
                  ) : (
                    r.text || " "
                  )}
                </span>
              </div>
            );
          })}
          {atEof && rows.length > 0 && <div className="bfv__edge">— end of file —</div>}
          {rows.length === 0 && <div className="bfv__edge" role="status">Loading…</div>}
        </div>
      )}
    </div>
  );
}
