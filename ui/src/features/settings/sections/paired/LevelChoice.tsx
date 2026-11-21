import { useId } from "react";
import { SegmentedControl } from "../../../../components/SegmentedControl/SegmentedControl";
import type { Level } from "../../../../router/devices";
import { LEVEL_DESC, VERB } from "./model";
import styles from "./LevelChoice.module.css";

export interface LevelChoiceProps {
  /** Who it's about: "spark". */
  subject: string;
  /** Where: "on this pop-os". */
  where: string;
  value: Level;
  onChange: (level: Level) => void;
  /** The levels offered; never admin in a dialog (Q2). */
  levels?: Level[];
  disabled?: boolean;
}

/**
 * A level in a dialog: the live sentence ("spark may control mads on this
 * pop-os") over a SegmentedControl, so no menu runs off the dialog, and what
 * the level means under it.
 */
export function LevelChoice({ subject, where, value, onChange, levels = ["none", "view", "chat", "control"], disabled }: LevelChoiceProps) {
  const id = useId();
  return (
    <div className={styles.root}>
      <div className={styles.sentence} id={id}>
        <b>{subject}</b> may <span className={styles.verb}>{VERB[value]}</span> mads {where}
      </div>
      <div>
        <SegmentedControl<Level>
          value={value}
          options={levels.map((l) => ({ value: l, label: VERB[l] }))}
          onChange={onChange}
          aria-label={`What ${subject} may do`}
          disabled={disabled}
        />
      </div>
      <div className={styles.desc}>{LEVEL_DESC[value]}</div>
    </div>
  );
}
