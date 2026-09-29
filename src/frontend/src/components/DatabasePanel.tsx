import { useEffect, useRef, useState } from "react";
import { ChevronDown, ChevronRight, Database, Loader2, Pencil, Plus, Table2, Trash2, Zap } from "lucide-react";
import { useStore } from "../state/store";
import { Db, errMessage } from "../lib/services";
import type { DbProfile, DbColumn, DbTable } from "../lib/services";
import { dbCredentialPresentation } from "../lib/dbCredential";
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

const tableKey = (table: DbTable) => `${table.schema}\0${table.name}`;
const tableLabel = (table: DbTable) => (table.schema && table.schema !== "main" ? `${table.schema}.${table.name}` : table.name);

export default function DatabasePanel() {
  const dbProfiles = useStore((s) => s.dbProfiles);
  const dbCredentialStatuses = useStore((s) => s.dbCredentialStatuses);
  const loadError = useStore((s) => s.dbError);
  const activeDbId = useStore((s) => s.activeDbId);
  const dbTables = useStore((s) => s.dbTables);
  const dbTablesLoading = useStore((s) => s.dbTablesLoading);
  const loadDbProfiles = useStore((s) => s.loadDbProfiles);
  const saveDbProfile = useStore((s) => s.saveDbProfile);
  const deleteDbProfile = useStore((s) => s.deleteDbProfile);
  const testDb = useStore((s) => s.testDb);
  const selectDb = useStore((s) => s.selectDb);
  const openDbQuery = useStore((s) => s.openDbQuery);
  const openDbTable = useStore((s) => s.openDbTable);

  const setStatus = useStore((s) => s.setStatus);
  const [draft, setDraft] = useState<DbProfile | null>(null);
  const [cols, setCols] = useState<Record<string, DbColumn[]>>({});
  const [openCols, setOpenCols] = useState<Record<string, boolean>>({});
  const [toDelete, setToDelete] = useState<DbProfile | null>(null);
  const [testing, setTesting] = useState<string | null>(null); // connection id being tested
  const [deleting, setDeleting] = useState<string | null>(null); // connection id being deleted
  const [saving, setSaving] = useState(false);
  const saveInFlight = useRef(false);
  const saveAttempt = useRef(0);
  const columnScope = useRef(0);
  const columnRequests = useRef<Record<string, number>>({});

  useEffect(() => {
    void loadDbProfiles();
  }, [loadDbProfiles]);

  // Reset the column cache when the active connection changes.
  useEffect(() => {
    columnScope.current++;
    columnRequests.current = {};
    setCols({});
    setOpenCols({});
  }, [activeDbId]);

  const toggleCols = async (table: DbTable) => {
    const key = `${table.schema}\0${table.name}`;
    const open = !openCols[key];
    const request = (columnRequests.current[key] ?? 0) + 1;
    columnRequests.current[key] = request;
    setOpenCols((o) => ({ ...o, [key]: open }));
    // Refetch on every expand — columns can change on disk, and the previous
    // cache was never invalidated, so a stale schema could linger.
    if (open && activeDbId) {
      const connectionId = activeDbId;
      const scope = columnScope.current;
      try {
        const c = await Db.ListColumns(connectionId, table.schema, table.name);
        if (
          columnScope.current !== scope ||
          columnRequests.current[key] !== request ||
          useStore.getState().activeDbId !== connectionId
        ) {
          return;
        }
        setCols((m) => ({ ...m, [key]: c }));
      } catch (e) {
        if (
          columnScope.current === scope &&
          columnRequests.current[key] === request &&
          useStore.getState().activeDbId === connectionId
        ) {
          setStatus(errMessage(e), "error");
        }
      }
    }
  };

  const patch = (p: Partial<DbProfile>) => setDraft((d) => (d ? { ...d, ...p } : d));

  const save = async () => {
    if (!draft || saveInFlight.current) return;
    const attempt = ++saveAttempt.current;
    saveInFlight.current = true;
    setSaving(true);
    const toSave = draft;
    // Drop the plaintext password from component state as soon as it's dispatched
    // — it shouldn't linger in the React tree (or survive a save failure).
    patch({ password: "" });
    try {
      const saved = await saveDbProfile(toSave);
      if (attempt !== saveAttempt.current) return;
      if (saved) {
        setDraft(null);
        void selectDb(saved.id);
      }
    } finally {
      if (attempt === saveAttempt.current) {
        saveInFlight.current = false;
        setSaving(false);
      }
    }
  };

  const cancelDraft = () => {
    if (saveInFlight.current) return;
    saveAttempt.current++;
    setDraft(null);
  };

  const openDraft = (profile: DbProfile) => {
    if (saveInFlight.current) return;
    saveAttempt.current++;
    setDraft(profile);
  };

  const runTest = async (id: string) => {
    setTesting(id);
    try {
      await testDb(id);
    } finally {
      setTesting(null);
    }
  };

  const openTable = (table: DbTable) => {
    if (!activeDbId) return;
    void openDbTable(activeDbId, table);
  };

  const isNetwork = draft && draft.kind !== "sqlite";
  const persistedDraft = draft?.id ? dbProfiles.find((profile) => profile.id === draft.id) : undefined;
  const credential = draft
    ? dbCredentialPresentation(draft, persistedDraft, draft.id ? dbCredentialStatuses[draft.id] : undefined)
    : null;

  return (
    <div className="db">
      {loadError && (
        <div className="db__loaderror" role="alert">
          {loadError}
        </div>
      )}
      <div className="db__toolbar">
        <button className="btn" onClick={() => openDraft(emptyProfile())} disabled={Boolean(loadError) || saving}>
          <Plus size={14} /> New connection
        </button>
      </div>

      {draft && (
        <div className="db__form">
          <label>
            Name
            <input value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="My database" disabled={saving} />
          </label>
          <label>
            Type
            <select value={draft.kind} onChange={(e) => patch({ kind: e.target.value })} disabled={saving}>
              <option value="sqlite">SQLite</option>
              <option value="postgres">PostgreSQL</option>
              <option value="mysql">MySQL</option>
            </select>
          </label>
          {draft.kind === "sqlite" && (
            <label>
              File path
              <input
                value={draft.file}
                spellCheck={false}
                onChange={(e) => patch({ file: e.target.value })}
                placeholder="C:\\path\\to\\data.db"
                disabled={saving}
              />
            </label>
          )}
          {isNetwork && (
            <>
              <div className="db__row2">
                <label>
                  Host
                  <input value={draft.host} onChange={(e) => patch({ host: e.target.value })} placeholder="localhost" disabled={saving} />
                </label>
                <label className="db__port">
                  Port
                  <input
                    type="number"
                    value={draft.port || ""}
                    onChange={(e) => patch({ port: Number(e.target.value) || 0 })}
                    placeholder={draft.kind === "postgres" ? "5432" : "3306"}
                    disabled={saving}
                  />
                </label>
              </div>
              <label>
                Database
                <input value={draft.database} onChange={(e) => patch({ database: e.target.value })} disabled={saving} />
              </label>
              <label>
                User
                <input value={draft.user} onChange={(e) => patch({ user: e.target.value })} disabled={saving} />
              </label>
              <label>
                Password{" "}
                {credential?.label ? (
                  <span className={credential.verified ? "db__keyset" : "db__keypending"}>{credential.label}</span>
                ) : null}
                <input
                  type="password"
                  value={draft.password ?? ""}
                  onChange={(e) => patch({ password: e.target.value })}
                  placeholder={credential?.placeholder ?? ""}
                  disabled={saving}
                />
              </label>
            </>
          )}
          <div className="db__formactions">
            <button className="btn btn--primary" onClick={() => void save()} disabled={!draft.name.trim() || Boolean(loadError) || saving}>
              {saving ? <Loader2 size={13} className="spin" /> : null} Save
            </button>
            <button className="btn" onClick={cancelDraft} disabled={saving}>
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="db__list">
        {dbProfiles.length === 0 && !draft && !loadError && <div className="db__empty">No connections yet.</div>}
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
                  disabled={deleting === p.id || saving}
                  onClick={() => openDraft({ ...emptyProfile(), ...p, password: "" })}
                >
                  <Pencil size={13} />
                </button>
                <button
                  className="icon-btn"
                  title="Delete"
                  disabled={deleting === p.id || Boolean(loadError) || saving}
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
                  <div key={tableKey(t)}>
                    <div className="db__table" title={`${t.type} ${tableLabel(t)}`}>
                      <button
                        className="db__coltoggle"
                        title="Show columns"
                        onClick={(e) => {
                          e.stopPropagation();
                          void toggleCols(t);
                        }}
                      >
                        {openCols[tableKey(t)] ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                      </button>
                      <Table2 size={13} color={t.type === "view" ? "var(--purple)" : "var(--cyan)"} />
                      <span
                        className="db__tablename"
                        role="button"
                        tabIndex={0}
                        onClick={() => openTable(t)}
                        onKeyDown={onActivate(() => openTable(t))}
                      >
                        {tableLabel(t)}
                      </span>
                    </div>
                    {openCols[tableKey(t)] &&
                      (cols[tableKey(t)] ?? []).map((c) => (
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
