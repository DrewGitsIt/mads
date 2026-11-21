import { Button } from "../../../../../components/Button/Button";
import { Caret } from "../../../../../components/Caret/Caret";
import { Dialog, DialogText } from "../../../../../components/Dialog/Dialog";
import styles from "./Pairing.module.css";

export interface PairChooserProps {
  host: string;
  onCopy: () => void;
  onPaste: () => void;
  onCancel: () => void;
}

/** Pair a device: copy this device's code, or paste another's. */
export function PairChooser({ host, onCopy, onPaste, onCancel }: PairChooserProps) {
  return (
    <Dialog
      title="Pair a device"
      onDismiss={onCancel}
      actions={
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      }
    >
      <DialogText>Both devices check each other's identity. Each one then decides for itself what the other may do on it.</DialogText>
      <div className={styles.choices}>
        <button type="button" className={styles.choice} onClick={onCopy}>
          <span className={styles.choiceIcon} aria-hidden="true">
            ⎘
          </span>
          <span>
            <span className={styles.choiceTitle}>Copy this device's pairing code</span>
            <span className={styles.choiceHelp}>
              Paste it on the other device. You approve it here and choose what it may do on this {host}.
            </span>
          </span>
          <Caret className={styles.choiceCaret} />
        </button>
        <button type="button" className={styles.choice} onClick={onPaste}>
          <span className={styles.choiceIcon} aria-hidden="true">
            ⎙
          </span>
          <span>
            <span className={styles.choiceTitle}>Paste a pairing code</span>
            <span className={styles.choiceHelp}>From the other device. It approves this {host} and chooses what it may do there.</span>
          </span>
          <Caret className={styles.choiceCaret} />
        </button>
      </div>
    </Dialog>
  );
}
