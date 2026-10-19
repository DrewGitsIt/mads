import { SegmentedControl } from "../../components/SegmentedControl/SegmentedControl";
import styles from "./ZoomLevelControl.module.css";

export type ZoomLevel = "overview" | "cards" | "focus";

export interface ZoomLevelControlProps {
  value: ZoomLevel;
  onChange: (level: ZoomLevel) => void;
  /** Focus needs a task to zoom into; off in Plan or with nothing to show. */
  focusDisabled: boolean;
  /** The narrow focus column: drops the gesture hint and tightens up to stay on one row. */
  compact?: boolean;
}

/** The Graph view's semantic zoom: Overview · Cards · Focus, bottom-left of the canvas. */
export function ZoomLevelControl({ value, onChange, focusDisabled, compact = false }: ZoomLevelControlProps) {
  return (
    <div className={[styles.zoomer, compact && styles.compact].filter(Boolean).join(" ")}>
      <SegmentedControl<ZoomLevel>
        aria-label="Zoom level"
        className={styles.group}
        value={value}
        onChange={onChange}
        options={[
          { value: "overview", label: "Overview" },
          { value: "cards", label: "Cards" },
          {
            value: "focus",
            label: "Focus",
            disabled: focusDisabled,
            title: focusDisabled ? "Click a task in a run to focus it" : undefined,
          },
        ]}
      />
      {!compact && <span className={styles.hint}>⌘ scroll / pinch</span>}
    </div>
  );
}
