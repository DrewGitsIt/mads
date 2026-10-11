import { Button } from "../../components/Button/Button";
import { SettingTag } from "./SettingRow";
import styles from "./SaveBar.module.css";

export interface SaveBarProps {
  /** Settings with unsaved edits; the bar renders only when this is > 0. */
  count: number;
  /** An unsaved edit is to a field that only applies on restart. */
  restartNeeded: boolean;
  saving: boolean;
  onDiscard: () => void;
  onSave: () => void;
}

/** Settings' one save affordance: pinned to the bottom of the content column while the draft has unsaved edits. */
export function SaveBar({ count, restartNeeded, saving, onDiscard, onSave }: SaveBarProps) {
  if (count === 0) return null;
  return (
    <div className={styles.bar}>
      <div className={styles.inner} role="region" aria-label="Unsaved changes">
        <div className={styles.msg}>
          Unsaved changes
          <span className={styles.count}>
            {count} setting{count === 1 ? "" : "s"}
          </span>
        </div>
        {restartNeeded && <SettingTag tone="warn">Restart needed</SettingTag>}
        <Button variant="ghost" onClick={onDiscard} disabled={saving}>
          Discard
        </Button>
        <Button variant="primary" onClick={onSave} disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}
