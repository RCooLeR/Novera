import { useStore } from "../state/store";
import ConfirmModal from "./ConfirmModal";

export default function ConfirmDeleteModal() {
  const pd = useStore((s) => s.pendingDelete);
  const confirm = useStore((s) => s.confirmDelete);
  const cancel = useStore((s) => s.cancelDelete);

  if (!pd) return null;

  return (
    <ConfirmModal
      title={`Delete ${pd.name}?`}
      body={
        <>
          <code>{pd.rel}</code> will be permanently deleted from disk. This can't be undone.
        </>
      }
      confirmLabel="Delete"
      danger
      onConfirm={() => void confirm()}
      onCancel={cancel}
    />
  );
}
