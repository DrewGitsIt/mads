import type { ReactNode } from "react";
import styles from "./SettingRow.module.css";

export interface SettingRowProps {
  label: ReactNode;
  /** Renders the label as <label htmlFor> — for inputs, Select, Switch. */
  htmlFor?: string;
  /** Gives the label this id instead — for controls named by aria-labelledby (SegmentedControl). */
  labelId?: string;
  help?: ReactNode;
  /** Inline after the label, e.g. a <SettingTag>Restart</SettingTag>. */
  tag?: ReactNode;
  error?: string;
  /** Control below the label instead of beside it, for wide controls. */
  stacked?: boolean;
  /** A dependent control: indented on a rail under the row it depends on. */
  nested?: boolean;
  /** Dims a nested row whose parent setting turns it off. */
  disabled?: boolean;
  children?: ReactNode;
}

/** One setting: label, tag and help on the left, its control on the right. */
export function SettingRow({
  label,
  htmlFor,
  labelId,
  help,
  tag,
  error,
  stacked,
  nested,
  disabled,
  children,
}: SettingRowProps) {
  const classes = [
    styles.row,
    stacked && styles.stacked,
    nested && styles.nested,
    disabled && styles.disabled,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={classes}>
      <div className={styles.text}>
        <div className={styles.labelLine}>
          {htmlFor ? (
            <label className={styles.label} htmlFor={htmlFor} id={labelId}>
              {label}
            </label>
          ) : (
            <span className={styles.label} id={labelId}>
              {label}
            </span>
          )}
          {tag}
        </div>
        {help && <div className={styles.help}>{help}</div>}
        {error && <div className={styles.error}>{error}</div>}
      </div>
      {children && <div className={styles.control}>{children}</div>}
    </div>
  );
}

export type SettingTagTone = "warn" | "info" | "muted";

/** Small uppercase marker beside a setting's label, e.g. "Restart". */
export function SettingTag({ tone = "muted", children }: { tone?: SettingTagTone; children: ReactNode }) {
  return <span className={[styles.tag, styles[tone]].join(" ")}>{children}</span>;
}
