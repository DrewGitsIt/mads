import styles from "./ObservationsInput.module.css";

export interface ObservationsInputProps {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}

/** Free-text observations to steer retro generation, controlled by RetroSurface. */
export function ObservationsInput({ value, onChange, disabled }: ObservationsInputProps) {
  return (
    <textarea
      className={styles.input}
      placeholder="Anything you noticed that the retro should take into account..."
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
      aria-label="Observations"
    />
  );
}
