import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";

export interface SessionDeleteConfirmDialogProps {
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/** Shared delete-confirm copy so the workspace kebab and rail row menu render byte-identical text. */
export function SessionDeleteConfirmDialog({
  open,
  onConfirm,
  onCancel,
}: SessionDeleteConfirmDialogProps) {
  return (
    <ConfirmDialog
      open={open}
      title="Delete this session?"
      description="Deleting permanently removes this session, its tasks, file-change history, and commit records. Provider transcripts and the event log survive. This can't be undone."
      confirmLabel="Delete"
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
