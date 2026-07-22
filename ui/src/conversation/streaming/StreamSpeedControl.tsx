import { useState } from "react";
import { Popover } from "../../components/Popover/Popover";
import { STREAM_SPEEDS, setStreamSpeed, useStreamSpeed, type StreamSpeed } from "./streamSpeed";
import styles from "./StreamSpeedControl.module.css";

const LABEL: Record<StreamSpeed, string> = {
  off: "native",
  slow: "slow",
  medium: "medium",
  fast: "fast",
};

const HINT: Record<StreamSpeed, string> = {
  off: "render as it arrives (model speed)",
  slow: "~10 tok/s",
  medium: "~20 tok/s",
  fast: "~30 tok/s",
};

/** Compact picker for the typewriter render rate. Global preference (localStorage). */
export function StreamSpeedControl() {
  const speed = useStreamSpeed();
  const [open, setOpen] = useState(false);

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={styles.trigger}
          title="Streaming speed"
          aria-haspopup="listbox"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={styles.key}>stream</span> {LABEL[speed]}
          <span className={styles.caret} aria-hidden="true">▾</span>
        </button>
      }
    >
      <div className={styles.menu} role="listbox">
        {STREAM_SPEEDS.map((s) => (
          <button
            key={s}
            type="button"
            role="option"
            aria-selected={s === speed}
            className={[styles.option, s === speed ? styles.selected : ""].filter(Boolean).join(" ")}
            onClick={() => {
              setStreamSpeed(s);
              setOpen(false);
            }}
          >
            <span className={styles.optLabel}>{LABEL[s]}</span>
            <span className={styles.optHint}>{HINT[s]}</span>
          </button>
        ))}
      </div>
    </Popover>
  );
}
