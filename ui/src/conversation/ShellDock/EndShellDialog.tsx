import { ConfirmDialog } from "../../components/ConfirmDialog/ConfirmDialog";
import type { ShellActivity } from "../TerminalRenderer/ptyBridge";
import styles from "./EndShellDialog.module.css";

export interface EndShellDialogProps {
  /** What is running in the shell being ended; undefined means no dialog. */
  activity: ShellActivity | undefined;
  /** The cancel button's label: the dock's keeps the shell by hiding it, Settings just keeps it. */
  cancelLabel: string;
  onConfirm: () => void;
  /** The cancel button. */
  onCancel: () => void;
  /** Escape or a backdrop click is neither choice: just close the question. */
  onDismiss: () => void;
}

/**
 * The question asked before ending a shell with something still running
 * (docs/SHELL-DOCK-DESIGN.md D4), listing what runs and the nohup caveat.
 * Shared by the dock's × and the Settings inventory's End, so the wording
 * can't drift between them.
 */
export function EndShellDialog({
  activity,
  cancelLabel,
  onConfirm,
  onCancel,
  onDismiss,
}: EndShellDialogProps) {
  return (
    <ConfirmDialog
      open={activity !== undefined}
      title={
        activity && activity.count > 1
          ? `${activity.count} processes are still running in this shell`
          : "A process is still running in this shell"
      }
      description={
        activity && (
          <>
            <ul className={styles.running}>
              {activity.processes.map((p) => (
                <li key={`${p.pid}-${p.command}`} className={styles.runningCommand} title={p.command}>
                  {p.command}
                </li>
              ))}
            </ul>
            <p>
              Ending the shell stops {activity.count > 1 ? "them" : "it"}. Anything started with{" "}
              <code>nohup</code>, <code>disown</code> or <code>setsid</code> may keep running.
            </p>
          </>
        )
      }
      confirmLabel="End shell"
      cancelLabel={cancelLabel}
      onConfirm={onConfirm}
      onCancel={onCancel}
      onDismiss={onDismiss}
    />
  );
}
