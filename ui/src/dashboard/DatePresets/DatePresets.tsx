import { Chip } from "../../components/Chip/Chip";
import styles from "./DatePresets.module.css";

export type DatePreset = "7d" | "30d" | "90d" | "all";

export const DATE_PRESETS: { id: DatePreset; label: string }[] = [
  { id: "7d", label: "7d" },
  { id: "30d", label: "30d" },
  { id: "90d", label: "90d" },
  { id: "all", label: "All" },
];

export interface DatePresetsProps {
  value: DatePreset;
  onChange: (preset: DatePreset) => void;
}

/** Preset chips scoping the cost chart and run history table (HANDOFF.md §2). */
export function DatePresets({ value, onChange }: DatePresetsProps) {
  return (
    <div className={styles.presets} role="group" aria-label="Date range">
      {DATE_PRESETS.map((preset) => (
        <Chip
          key={preset.id}
          tone={value === preset.id ? "info" : "neutral"}
          pressed={value === preset.id}
          onClick={() => onChange(preset.id)}
        >
          {preset.label}
        </Chip>
      ))}
    </div>
  );
}
