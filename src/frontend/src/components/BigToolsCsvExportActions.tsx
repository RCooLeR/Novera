interface BigToolsCsvExportActionsProps {
  disabled: boolean;
  numberKeys: boolean;
  onNumberKeysChange: (value: boolean) => void;
  onExportJSONL: () => void;
}

/**
 * CSV exports that are safe to expose from Big Tools.
 *
 * SQLite and XLSX are intentionally not actions here: their backend paths
 * remain fail-closed until output publication is qualified end to end.
 */
export default function BigToolsCsvExportActions({
  disabled,
  numberKeys,
  onNumberKeysChange,
  onExportJSONL,
}: BigToolsCsvExportActionsProps) {
  return (
    <>
      <div className="bigtools__row">
        <label className="bigtools__check">
          <input
            type="checkbox"
            checked={numberKeys}
            onChange={(event) => onNumberKeysChange(event.target.checked)}
          />{" "}
          numeric JSON keys
        </label>
      </div>
      <div className="bigtools__row">
        <button type="button" className="btn" disabled={disabled} onClick={onExportJSONL}>
          JSONL
        </button>
      </div>
      <div className="muted" role="note">
        SQLite and XLSX exports are unavailable until their output publication
        paths are qualified.
      </div>
    </>
  );
}
