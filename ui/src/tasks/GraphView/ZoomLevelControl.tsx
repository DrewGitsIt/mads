import { SegmentedControl } from "../../components/SegmentedControl/SegmentedControl";
import styles from "./ZoomLevelControl.module.css";

export type ZoomLevel = "overview" | "cards" | "focus";

export interface ZoomLevelControlProps {
  value: ZoomLevel;
  onChange: (level: ZoomLevel) => void;
  /** Focus needs a task to zoom into; off in Plan or with nothing to show. */
  focusDisabled: boolean;
}

/** The Graph view's semantic zoom: Overview · Cards · Focus, bottom-left of the canvas. */
export function ZoomLevelControl({ value, onChange, focusDisabled }: ZoomLevelControlProps) {
  return (
    <div className={styles.zoomer}>
      <SegmentedControl<ZoomLevel>
        aria-label="Zoom level"
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
      <span className={styles.hint}>⌘ scroll / pinch</span>
    </div>
  );
}
