import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";

export interface SessionUnlinkConfirmDialogProps {
  open: boolean;
  sessionName: string;
  onConfirm: () => void;
  onCancel: () => void;
}

/** Confirms deleting one link (design decision D6: reuses ConfirmDialog, not the chooser dialog). */
export function SessionUnlinkConfirmDialog({
  open,
  sessionName,
  onConfirm,
  onCancel,
}: SessionUnlinkConfirmDialogProps) {
  return (
    <ConfirmDialog
      open={open}
      title="Unlink this session?"
      description={`This removes the relationship with "${sessionName}". Neither session is deleted.`}
      confirmLabel="Unlink"
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
