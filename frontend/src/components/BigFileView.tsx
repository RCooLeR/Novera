import { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowDownToLine, Binary, FileText, Pencil, Search as SearchIcon } from "lucide-react";
import { BigFile, errMessage } from "../lib/services";
import type { StagingState } from "../lib/services";
import { highlighterFor } from "../lib/lineHighlight";
import type { LineTokenizer, Seg } from "../lib/lineHighlight";
import { SerialTaskQueue } from "../lib/serialTaskQueue";
import { LatestRequest } from "../lib/latestRequest";
import { useStore } from "../state/store";

const editEnc = new TextEncoder();
const byteLen = (s: string) => editEnc.encode(s).length; // UTF-8 byte length (StageEdit origLen)
const EDIT_WINDOW_BYTES = 256 * 1024; // editable window budget (textarea-friendly)

/* ---------------------------------------------------------------------------
 * BigFileView — windowed viewer for files of ANY size, backed by the ported
 * Novera's large-file streaming engine (internal/bigfile). The whole file never enters
 * memory: the backend serves bounded, line-aligned windows; this component
 * keeps a bounded ring of rows in the DOM and pages new windows in/out as the
 * user scrolls. Adds true line numbers, in-file search, go-to, a hex mode,
 * follow-tail, and windowed editing (UTF-8/LF files) with crash-safe in-place
 * patch / save-as-copy — the staging + save machinery lives in the Go engine.
 * ------------------------------------------------------------------------- */

const ROW_H = 18; // px — must match .bfv__row height in global.css
const WINDOW_BYTES = 1 << 20; // 1 MiB text window budget
const HEX_BYTES = 64 * 1024; // hex window budget
const MAX_ROWS = 24000; // bounded DOM ring; trims the far edge past this
const EDGE_PX = 700; // distance from an edge that triggers a page load
const PREV_LOOKBACK = 128 * 1024; // backward step when paging up (kept small: bridged to the anchor)

type Mode = "text" | "hex";
type Row = { gutter: string; off: number; text: string; segs?: Seg[] };
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
  const stageTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
	const stageQueueRef = useRef(new SerialTaskQueue());
  const viewRequestsRef = useRef(new LatestRequest());
  const sourceVersionRef = useRef(sourceVersion);
  // Deferred scroll op applied after rows commit: keep position stable on
  // prepend/trim, or snap to top/bottom on reset.
  const scrollOpRef = useRef<{ kind: "delta" | "top" | "bottom"; value: number } | null>(null);

  const fid = fileId; // stable alias for callbacks

  // ---- window → rows ----
  // Tokenise each line once, at load time (not per render), so syntax colours
  // are computed a window at a time and reused as the user scrolls.
  const textRows = (w: { text: string; lineNumbers: number[]; lineOffsets: number[] }): Row[] => {
    const lines = w.text.length ? w.text.split("\n") : [];
    const tok = tokenizerRef.current;
    return lines.map((t, i) => ({
      gutter: String(w.lineNumbers[i] ?? ""),
      off: Number(w.lineOffsets[i] ?? 0),
      text: t,
      segs: tok ? tok(t) : undefined,
    }));
  };

  const loadText = useCallback(async (id: string, startByte: number): Promise<Loaded> => {
    const w = await BigFile.GetWindow(id, startByte, WINDOW_BYTES);
    return { rows: textRows(w), nextByte: w.nextByte, atBof: w.atBof, atEof: w.atEof, approx: w.approx };
  }, []);

  const loadTextPrev = useCallback(async (id: string, currentStart: number): Promise<Loaded> => {
    // GetPrevWindow is line-capped (windowLineTarget) and ignores the byte
    // budget, so its window can stop well short of currentStart for dense text.
    // Start a small step back, then page FORWARD bridging any gap, keeping only
    // rows strictly before currentStart — so the prepended block is contiguous
    // with the existing top row (no dropped lines / line-number jumps).
    const first = await BigFile.GetPrevWindow(id, currentStart, PREV_LOOKBACK);
    const out = textRows(first).filter((r) => r.off < currentStart);
    let cur = Number(first.nextByte);
    let approximate = first.approx;
    // Termination is guaranteed by the no-progress break; the counter is only a
    // runaway backstop. It must exceed the worst-case window count for the
    // lookback (128 KiB of 1-byte lines ≈ 66 windows of 2000 lines) so it never
    // trips on a real file and leaves a gap.
    let guard = 0;
    while (cur < currentStart && guard++ < 512) {
      const w = await BigFile.GetWindow(id, cur, WINDOW_BYTES);
      approximate ||= w.approx;
      for (const r of textRows(w)) if (r.off < currentStart) out.push(r);
      if (Number(w.nextByte) <= cur) break; // no forward progress — avoid a spin
      cur = Number(w.nextByte);
    }
    return { rows: out, nextByte: currentStart, atBof: first.atBof, atEof: false, approx: approximate };
  }, []);

  const loadHex = useCallback(async (id: string, startByte: number): Promise<Loaded> => {
    const h = await BigFile.GetHexWindow(id, startByte, HEX_BYTES);
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
      const h = await BigFile.GetHexWindow(id, start, HEX_BYTES);
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
        await BigFile.RefreshFile(fid);
        const total = Number(await BigFile.FileSize(fid));
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
      viewRequestsRef.current.invalidate();
			setLargeFileState(tabPath, { sessionId: "" });
			if (openedId) void BigFile.CloseFile(openedId);
		};
	}, [abs, loadHex, loadText, setLargeFileState, tabPath]);

	useEffect(() => {
		setLargeFileState(tabPath, {
			sessionId: fid,
			dirty: hasStagedEdits,
			inPlaceEligible: !!staging?.inPlaceEligible,
		});
	}, [fid, hasStagedEdits, setLargeFileState, staging?.inPlaceEligible, tabPath]);

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
  }, [atEof, fid, loadAt]);

  const prependPrev = useCallback(async () => {
    if (loadingRef.current || atBof || !fid || rows.length === 0) return;
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
  }, [atBof, fid, rows, loadPrevAt]);

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
      const generation = viewRequestsRef.current.begin();
      setFollow(false);
      setNote("Searching…");
      try {
        // Seed from the last hit's edge so a direction flip steps to the
        // adjacent match instead of re-finding the current one (FindNext is
        // at/after fromByte; FindPrev is strictly before beforeByte).
        const last = lastHitRef.current;
        const from = backward ? (last ? last.start : cursorRef.current) : last ? last.end : cursorRef.current;
        const hit = backward
          ? await BigFile.FindPrev(fid, query, from, regex, caseSensitive, false)
          : await BigFile.FindNext(fid, query, from, regex, caseSensitive, false);
        if (!viewRequestsRef.current.isCurrent(generation)) return;
        if (hit.unsupported) {
          setNote(hit.message || "Search not supported for this file.");
          return;
        }
        if (hit.timedOut) {
          setNote("Search timed out.");
          return;
        }
        if (!hit.found) {
          setNote("No matches.");
          return;
        }
        const off = Number(hit.offset);
        lastHitRef.current = { start: off, end: off + Math.max(1, hit.length) };
        cursorRef.current = off;
        setNote(`Line ${Number(hit.line).toLocaleString()}`);
        if (mode === "hex") await reset(fid, off - (off % 16), "top", off);
        else await reset(fid, off, "top", off);
      } catch (e) {
        if (viewRequestsRef.current.isCurrent(generation)) setNote(errMessage(e));
      }
    },
    [fid, query, regex, caseSensitive, mode, reset],
  );

  // ---- go to line / 0xOFFSET / percent ----
  const runGoto = useCallback(async () => {
    if (!fid) return;
    const v = gotoVal.trim();
    if (!v) return;
    const generation = viewRequestsRef.current.begin();
    setFollow(false);
    try {
      let byte = 0;
      if (/^0x[0-9a-f]+$/i.test(v)) byte = parseInt(v, 16);
      else if (v.endsWith("%")) byte = Math.floor((size * Math.min(100, Math.max(0, parseFloat(v)))) / 100);
      else {
        const line = parseInt(v, 10);
        if (!Number.isFinite(line)) return;
        byte = Number(await BigFile.ResolveLine(fid, line));
      }
      if (!viewRequestsRef.current.isCurrent(generation)) return;
      if (mode === "hex") byte -= byte % 16;
      cursorRef.current = Math.max(0, byte); // search continues from where we jumped
      lastHitRef.current = null;
      setNote("");
      await reset(fid, Math.max(0, byte), "top", null);
    } catch (e) {
      if (viewRequestsRef.current.isCurrent(generation)) setNote(errMessage(e));
    }
  }, [fid, gotoVal, size, mode, reset]);

  const switchMode = useCallback(
    async (m: Mode) => {
      if (m === mode || !fid) return;
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
    [mode, fid, rows, loadHex, loadText],
  );

  // ---- editing ----
  const refreshStaging = useCallback(async () => {
    if (!fid) return;
    try {
      setStaging(await BigFile.GetStagingState(fid));
    } catch {
      /* ignore */
    }
  }, [fid]);

  const loadEditWindow = useCallback(
    async (start: number) => {
      if (!fid) return;
      const w = await BigFile.GetEditWindow(fid, start, EDIT_WINDOW_BYTES);
      setEditStart(Number(w.startByte));
      editOrigLenRef.current = Number(w.nextByte) - Number(w.startByte);
      editTextRef.current = w.text;
      setEditText(w.text);
      void refreshStaging();
    },
    [fid, refreshStaging],
  );

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
				setStaging(st);
				setLargeFileState(tabPath, { dirty: st.editCount > 0, inPlaceEligible: st.inPlaceEligible });
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

  const enterEdit = useCallback(() => {
    if (!editable || !fid) return;
		setFollow(false);
    setEditMode(true);
    void loadEditWindow(editMode ? editStart : (rows[0]?.off ?? 0));
  }, [editable, fid, editMode, editStart, loadEditWindow, rows]);

  const exitEdit = useCallback(async () => {
    clearTimeout(stageTimerRef.current);
		setSaving(true);
		try {
			if (await stageNow()) setEditMode(false);
		} finally {
			setSaving(false);
		}
  }, [stageNow]);

  const doDiscard = useCallback(async () => {
    if (!fid) return;
    clearTimeout(stageTimerRef.current);
		setSaving(true);
    try {
			await stageQueueRef.current.drain();
      const st = await BigFile.DiscardEdits(fid);
      setStaging(st);
			setLargeFileState(tabPath, { dirty: false, inPlaceEligible: false });
      if (editMode) await loadEditWindow(editStart);
      else void reset(fid, rows[0]?.off ?? 0, "top", null);
      setNote("Discarded staged edits.");
    } catch (e) {
      setNote(errMessage(e));
		} finally {
			setSaving(false);
    }
	}, [fid, editMode, editStart, loadEditWindow, reset, rows, setLargeFileState, tabPath]);

  const doSavePatch = useCallback(async () => {
    if (!fid) return;
    if (staleOnDisk) {
      setNote("Save in place is blocked because the source changed. Save a copy or discard and reload.");
      return;
    }
    clearTimeout(stageTimerRef.current);
    setSaving(true);
    try {
			if (!(await stageNow())) return;
      const r = await BigFile.SavePatch(fid);
      setNote(`Saved in place · ${Number(r.bytesWritten).toLocaleString()} bytes patched`);
      setStaging(null);
			setLargeFileState(tabPath, { dirty: false, inPlaceEligible: false });
      setEditMode(false);
      void reset(fid, rows[0]?.off ?? 0, "top", null); // file reopened under same id
    } catch (e) {
      setNote(errMessage(e));
    } finally {
      setSaving(false);
    }
	}, [fid, staleOnDisk, stageNow, reset, rows, setLargeFileState, tabPath]);

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
    // Jump to the live tail. A single window is line-capped (and hex reads only
    // HEX_BYTES), so starting a fixed amount back can stop short of EOF — page
    // forward until atEof, keeping the last MAX_ROWS rows, then snap to bottom.
    const jumpTail = async (total: number, generation: number) => {
      const hex = modeRef.current === "hex";
      const back = hex ? HEX_BYTES : 256 * 1024;
      let cur = Math.max(0, total - back);
      const acc: Row[] = [];
      let bof = cur <= 0;
      let reachedEof = false;
      let approximate = false;
      let guard = 0;
      while (guard++ < 512) {
        const r = hex ? await loadHex(fid, cur) : await loadText(fid, cur);
        if (cancelled || !viewRequestsRef.current.isCurrent(generation)) return;
        acc.push(...r.rows);
        approximate ||= r.approx;
        tailNextRef.current = Number(r.nextByte);
        if (r.atEof) {
          reachedEof = true;
          break;
        }
        if (Number(r.nextByte) <= cur) break; // no progress
        cur = Number(r.nextByte);
      }
      if (cancelled || !viewRequestsRef.current.isCurrent(generation)) return;
      let trimmed = acc;
      if (acc.length > MAX_ROWS) {
        trimmed = acc.slice(acc.length - MAX_ROWS);
        bof = false;
      }
      setAtBof(bof);
      setAtEof(reachedEof); // only claim EOF if we actually reached it
      setApprox(approximate);
      scrollOpRef.current = { kind: "bottom", value: 0 };
      setRows(trimmed);
    };
    void (async () => {
      const generation = viewRequestsRef.current.begin();
      try {
        const total = Number(await BigFile.FileSize(fid));
        if (cancelled || !viewRequestsRef.current.isCurrent(generation)) return;
        setSize(total);
        await jumpTail(total, generation);
      } catch {
        /* ignore */
      }
    })();
    const t = setInterval(() => {
      void (async () => {
        try {
          const total = Number(await BigFile.FileSize(fid));
          if (cancelled || total <= size) return;
          const generation = viewRequestsRef.current.begin();
          await BigFile.RefreshFile(fid);
          if (cancelled || !viewRequestsRef.current.isCurrent(generation)) return;
          setSize(total);
          await jumpTail(total, generation);
        } catch {
          /* ignore */
        }
      })();
    }, 1200);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
	}, [follow, fid, hasStagedEdits, size, loadHex, loadText]);

  if (error) return <div className="editor-placeholder">{error}</div>;

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
            <SearchIcon size={13} className="bfv__searchicon" />
            <input
              className="bfv__input"
              placeholder="Find…"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                lastHitRef.current = null; // new query → search from the current anchor again
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") void runFind(e.shiftKey);
              }}
            />
            <button className={`bfv__opt${regex ? " on" : ""}`} title="Regex" onClick={() => setRegex((v) => !v)}>
              .*
            </button>
            <button className={`bfv__opt${caseSensitive ? " on" : ""}`} title="Match case" onClick={() => setCaseSensitive((v) => !v)}>
              Aa
            </button>
            <button className="bfv__btn" title="Find previous" onClick={() => void runFind(true)}>
              ↑
            </button>
            <button className="bfv__btn" title="Find next" onClick={() => void runFind(false)}>
              ↓
            </button>
          </div>
        )}

        {!editMode && (
          <input
            className="bfv__input bfv__goto"
            placeholder="line / 0x / %"
            value={gotoVal}
            onChange={(e) => setGotoVal(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void runGoto();
            }}
            title="Go to line number, 0xHEX offset, or percent"
          />
        )}

        {!editMode && !binaryHint && (
          <button
            className={`bfv__toggle${mode === "hex" ? " on" : ""}`}
            title="Toggle hex view"
            onClick={() => void switchMode(mode === "hex" ? "text" : "hex")}
          >
            {mode === "hex" ? <FileText size={14} /> : <Binary size={14} />}
          </button>
        )}
        {!editMode && (
					<button
						className={`bfv__toggle${follow ? " on" : ""}`}
						disabled={hasStagedEdits}
						title={hasStagedEdits ? "Save or discard staged edits before following the live file" : "Follow tail (live)"}
						onClick={() => setFollow((v) => !v)}
					>
            <ArrowDownToLine size={14} />
          </button>
        )}
        {!binary && (
          <button
            className={`bfv__toggle${editMode ? " on" : ""}`}
            disabled={!editable}
            title={editable ? "Edit file" : `Editing needs a UTF-8 / LF file (this is ${encoding || "?"})`}
            onClick={() => (editMode ? void exitEdit() : enterEdit())}
          >
            <Pencil size={14} />
          </button>
        )}
      </div>

      {note && <div className="bfv__note">{note}</div>}

      {staleOnDisk && (
        <div className="bfv__editbanner" role="alert">
          <span>The source changed on disk. In-place save is blocked.</span>
          <span className="bfv__spacer" />
          <button
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
          <button className="btn" onClick={enterEdit}>
            Resume
          </button>
          {staging.inPlaceEligible && (
            <button className="btn" disabled={saving || staleOnDisk} onClick={() => void doSavePatch()}>
              Save in place
            </button>
          )}
          <button className="btn" disabled={saving} onClick={() => void doSaveCopy()}>
            Save as copy…
          </button>
          <button className="btn btn--danger" disabled={saving} onClick={() => void doDiscard()}>
            Discard
          </button>
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
              {staging && staging.editCount > 0 && !staging.inPlaceEligible ? " · length changed" : ""}
            </span>
            <span className="bfv__spacer" />
            <button
              className="btn"
              disabled={saving || staleOnDisk || !staging?.inPlaceEligible || !staging?.editCount}
              title={staging?.inPlaceEligible ? "Apply edits in place (crash-safe)" : "Length changed — use Save as copy"}
              onClick={() => void doSavePatch()}
            >
              Save in place
            </button>
            <button className="btn" disabled={saving || !staging?.editCount} onClick={() => void doSaveCopy()}>
              Save as copy…
            </button>
            <button className="btn" disabled={saving || !staging?.editCount} onClick={() => void doDiscard()}>
              Discard
            </button>
            <button className="btn btn--primary" disabled={saving} onClick={() => void exitEdit()}>
              Done
            </button>
          </div>
          <textarea
            className="bfv__edit"
            value={editText}
				disabled={saving}
            spellCheck={false}
            wrap="off"
            onChange={(e) => onEditChange(e.target.value)}
          />
        </div>
      ) : (
        <div className="bfv__body" ref={scrollRef} onScroll={onScroll}>
          {!atBof && <div className="bfv__edge">↑ more above</div>}
          {rows.map((r, i) => {
            const next = rows[i + 1]?.off ?? Infinity;
            const hit = highlightOff != null && r.off <= highlightOff && highlightOff < next;
            return (
              <div className={`bfv__row${hit ? " hit" : ""}`} key={`${r.off}-${i}`}>
                <span className="bfv__gutter">{r.gutter}</span>
                <span className="bfv__text">{r.segs ? r.segs.map((s, j) => (s.c ? <span key={j} className={s.c}>{s.t}</span> : <Fragment key={j}>{s.t}</Fragment>)) : (r.text || " ")}</span>
              </div>
            );
          })}
          {atEof && rows.length > 0 && <div className="bfv__edge">— end of file —</div>}
          {rows.length === 0 && <div className="bfv__edge">Loading…</div>}
        </div>
      )}
    </div>
  );
}
