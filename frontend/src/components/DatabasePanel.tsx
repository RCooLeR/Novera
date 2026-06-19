import { useEffect, useState } from "react";
import { ChevronDown, ChevronRight, Database, Loader2, Pencil, Plus, Table2, Trash2, Zap } from "lucide-react";
import { useStore } from "../state/store";
import { Db, errMessage } from "../lib/services";
import type { DbProfile, DbColumn } from "../lib/services";
import ConfirmModal from "./ConfirmModal";

// Enter/Space activate a non-button clickable so it's keyboard-operable.
function onActivate(fn: () => void) {
  return (e: React.KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      fn();
    }
  };
}

const emptyProfile = (): DbProfile => ({
  id: "",
  name: "",
  kind: "sqlite",
  file: "",
  host: "",
  port: 0,
  database: "",
  user: "",
  sslMode: "",
  secretRef: "",
  password: "",
});

export default function DatabasePanel() {
  const dbProfiles = useStore((s) => s.dbProfiles);
  const activeDbId = useStore((s) => s.activeDbId);
  const dbTables = useStore((s) => s.dbTables);
  const dbTablesLoading = useStore((s) => s.dbTablesLoading);
  const loadDbProfiles = useStore((s) => s.loadDbProfiles);
  const saveDbProfile = useStore((s) => s.saveDbProfile);
  const deleteDbProfile = useStore((s) => s.deleteDbProfile);
  const testDb = useStore((s) => s.testDb);
  const selectDb = useStore((s) => s.selectDb);
  const openDbQuery = useStore((s) => s.openDbQuery);
  const setDbSql = useStore((s) => s.setDbSql);

  const setStatus = useStore((s) => s.setStatus);
  const [draft, setDraft] = useState<DbProfile | null>(null);
  const [cols, setCols] = useState<Record<string, DbColumn[]>>({});
  const [openCols, setOpenCols] = useState<Record<string, boolean>>({});
  const [toDelete, setToDelete] = useState<DbProfile | null>(null);
  const [testing, setTesting] = useState<string | null>(null); // connection id being tested
  const [deleting, setDeleting] = useState<string | null>(null); // connection id being deleted

  useEffect(() => {
    void loadDbProfiles();
    // Surface a corrupt-profiles-file load error (otherwise connections silently vanish).
    void Db.LoadError().then((msg) => {
      if (msg) setStatus(msg, "error");
    });
  }, [loadDbProfiles, setStatus]);

  // Reset the column cache when the active connection changes.
  useEffect(() => {
    setCols({});
    setOpenCols({});
  }, [activeDbId]);

  const toggleCols = async (table: string) => {
    const open = !openCols[table];
    setOpenCols((o) => ({ ...o, [table]: open }));
    // Refetch on every expand — columns can change on disk, and the previous
    // cache was never invalidated, so a stale schema could linger.
    if (open && activeDbId) {
      try {
        const c = await Db.ListColumns(activeDbId, table);
        setCols((m) => ({ ...m, [table]: c }));
      } catch (e) {
        setStatus(errMessage(e), "error");
      }
    }
  };

  const patch = (p: Partial<DbProfile>) => setDraft((d) => (d ? { ...d, ...p } : d));

  const save = async () => {
    if (!draft) return;
    const toSave = draft;
    // Drop the plaintext password from component state as soon as it's dispatched
    // — it shouldn't linger in the React tree (or survive a save failure).
    patch({ password: "" });
    const saved = await saveDbProfile(toSave);
    if (saved) {
      setDraft(null);
      void selectDb(saved.id);
    }
  };

  const runTest = async (id: string) => {
    setTesting(id);
    try {
      await testDb(id);
    } finally {
      setTesting(null);
    }
  };

  const openTable = (table: string) => {
    if (!activeDbId) return;
    setDbSql(activeDbId, `SELECT * FROM ${table} LIMIT 100`);
    openDbQuery(activeDbId);
  };

  const isNetwork = draft && draft.kind !== "sqlite";

  return (
    <div className="db">
      <div className="db__toolbar">
        <button className="btn" onClick={() => setDraft(emptyProfile())}>
          <Plus size={14} /> New connection
        </button>
      </div>

      {draft && (
        <div className="db__form">
          <label>
            Name
            <input value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="My database" />
          </label>
          <label>
            Type
            <select value={draft.kind} onChange={(e) => patch({ kind: e.target.value })}>
              <option value="sqlite">SQLite</option>
              <option value="postgres">PostgreSQL</option>
              <option value="mysql">MySQL</option>
            </select>
          </label>
          {draft.kind === "sqlite" && (
            <label>
              File path
              <input value={draft.file} spellCheck={false} onChange={(e) => patch({ file: e.target.value })} placeholder="C:\\path\\to\\data.db" />
            </label>
          )}
          {isNetwork && (
            <>
              <div className="db__row2">
                <label>
                  Host
                  <input value={draft.host} onChange={(e) => patch({ host: e.target.value })} placeholder="localhost" />
                </label>
                <label className="db__port">
                  Port
                  <input
                    type="number"
                    value={draft.port || ""}
                    onChange={(e) => patch({ port: Number(e.target.value) || 0 })}
                    placeholder={draft.kind === "postgres" ? "5432" : "3306"}
                  />
                </label>
              </div>
              <label>
                Database
                <input value={draft.database} onChange={(e) => patch({ database: e.target.value })} />
              </label>
              <label>
                User
                <input value={draft.user} onChange={(e) => patch({ user: e.target.value })} />
              </label>
              <label>
                Password {draft.secretRef ? <span className="db__keyset">stored</span> : null}
                <input
                  type="password"
                  value={draft.password ?? ""}
                  onChange={(e) => patch({ password: e.target.value })}
                  placeholder={draft.secretRef ? "•••••• (stored — type to replace)" : ""}
                />
              </label>
            </>
          )}
          <div className="db__formactions">
            <button className="btn btn--primary" onClick={() => void save()} disabled={!draft.name.trim()}>
              Save
            </button>
            <button className="btn" onClick={() => setDraft(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="db__list">
        {dbProfiles.length === 0 && !draft && <div className="db__empty">No connections yet.</div>}
        {dbProfiles.map((p) => (
          <div key={p.id}>
            <div
              className={`db__conn ${activeDbId === p.id ? "active" : ""}`}
              role="button"
              tabIndex={0}
              onClick={() => {
                void selectDb(p.id);
                openDbQuery(p.id);
              }}
              onKeyDown={onActivate(() => {
                void selectDb(p.id);
                openDbQuery(p.id);
              })}
              title={`${p.kind} · ${p.name}`}
            >
              <Database size={14} />
              <span className="db__connname">{p.name}</span>
              <span className="db__kind">{p.kind}</span>
              <span className="db__connactions" onClick={(e) => e.stopPropagation()}>
                <button
                  className="icon-btn"
                  title="Test connection"
                  disabled={testing === p.id || deleting === p.id}
                  onClick={() => void runTest(p.id)}
                >
                  {testing === p.id ? <Loader2 size={13} className="spin" /> : <Zap size={13} />}
                </button>
                <button
                  className="icon-btn"
                  title="Edit"
                  disabled={deleting === p.id}
                  onClick={() => setDraft({ ...emptyProfile(), ...p, password: "" })}
                >
                  <Pencil size={13} />
                </button>
                <button
                  className="icon-btn"
                  title="Delete"
                  disabled={deleting === p.id}
                  onClick={() => setToDelete(p)}
                >
                  {deleting === p.id ? <Loader2 size={13} className="spin" /> : <Trash2 size={13} />}
                </button>
              </span>
            </div>
            {activeDbId === p.id && (
              <div className="db__tables">
                {dbTablesLoading ? (
                  <div className="db__tablesempty">
                    <Loader2 size={12} className="spin" /> Loading tables…
                  </div>
                ) : (
                  dbTables.length === 0 && <div className="db__tablesempty">No tables</div>
                )}
                {dbTables.map((t) => (
                  <div key={t.name}>
                    <div className="db__table" title={`${t.type} ${t.name}`}>
                      <button
                        className="db__coltoggle"
                        title="Show columns"
                        onClick={(e) => {
                          e.stopPropagation();
                          void toggleCols(t.name);
                        }}
                      >
                        {openCols[t.name] ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                      </button>
                      <Table2 size={13} color={t.type === "view" ? "var(--purple)" : "var(--cyan)"} />
                      <span
                        className="db__tablename"
                        role="button"
                        tabIndex={0}
                        onClick={() => openTable(t.name)}
                        onKeyDown={onActivate(() => openTable(t.name))}
                      >
                        {t.name}
                      </span>
                    </div>
                    {openCols[t.name] &&
                      (cols[t.name] ?? []).map((c) => (
                        <div key={c.name} className="db__col">
                          <span className="db__colname">{c.name}</span>
                          <span className="db__coltype">
                            {c.type}
                            {c.nullable ? "" : " ·NN"}
                          </span>
                        </div>
                      ))}
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>

      {toDelete && (
        <ConfirmModal
          title={`Delete connection "${toDelete.name}"?`}
          body="This removes the saved connection and its stored password. It can't be undone."
          confirmLabel="Delete"
          danger
          onConfirm={() => {
            const id = toDelete.id;
            setToDelete(null);
            setDeleting(id);
            void deleteDbProfile(id).finally(() => setDeleting(null));
          }}
          onCancel={() => setToDelete(null)}
        />
      )}
    </div>
  );
}
