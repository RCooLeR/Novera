interface LargeFileSaveActionsProps {
  saving: boolean;
  hasEdits: boolean;
  discardDanger?: boolean;
  onSaveCopy: () => void;
  onDiscard: () => void;
}

/**
 * The supported large-file edit settlement actions.
 *
 * Direct source replacement is intentionally absent until its recovery and
 * lifecycle guarantees are strong enough to expose as a product feature.
 */
export default function LargeFileSaveActions({
  saving,
  hasEdits,
  discardDanger = false,
  onSaveCopy,
  onDiscard,
}: LargeFileSaveActionsProps) {
  const disabled = saving || !hasEdits;
  return (
    <>
      <button
        type="button"
        className="btn"
        disabled={disabled}
        title="Write the staged edits to a new file without modifying the source"
        onClick={onSaveCopy}
      >
        Save as copy…
      </button>
      <button
        type="button"
        className={`btn${discardDanger ? " btn--danger" : ""}`}
        disabled={disabled}
        onClick={onDiscard}
      >
        Discard
      </button>
    </>
  );
}
