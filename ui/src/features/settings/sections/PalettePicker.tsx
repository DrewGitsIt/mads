import { useEffect, useRef, type KeyboardEvent } from "react";
import { useAppliedTheme } from "../theme/useAppliedTheme";
import type { PaletteName } from "../../../router/types";
import styles from "./PalettePicker.module.css";

const PALETTES: { value: PaletteName; label: string; description: string }[] = [
  { value: "clay", label: "Clay", description: "Warm umber with a terracotta accent. Paper and ink." },
  { value: "space", label: "Space", description: "Deep indigo-black with a violet accent. Night shift." },
  { value: "glacier", label: "Glacier", description: "Cool slate with an icy blue accent. Crisp and quiet." },
];

export interface PalettePickerProps {
  /** The saved palette: the only card that reads checked, hover or not. */
  value: PaletteName;
  onChoose: (palette: PaletteName) => void;
  /** Try a palette on (pointer hover), or null to go back to the saved one. */
  onPreview: (palette: PaletteName | null) => void;
  "aria-labelledby"?: string;
}

/**
 * Palette choice as preview cards: each draws a tiny app in its own palette
 * (scoped [data-palette]/[data-theme], so tokens.css resolves per card) at
 * the theme currently applied. Pointer hover tries a palette on across the
 * app; keyboard focus doesn't, so tabbing past never flashes the page.
 * Radio-group keys as in SegmentedControl: arrows move focus and choose.
 */
export function PalettePicker({ value, onChoose, onPreview, ...aria }: PalettePickerProps) {
  const theme = useAppliedTheme();
  const buttonsRef = useRef<(HTMLButtonElement | null)[]>([]);
  const selectedIndex = PALETTES.findIndex((p) => p.value === value);
  const tabStop = Math.max(selectedIndex, 0);

  // Leaving Settings mid-hover must not strand the app in a tried-on palette.
  const previewing = useRef(false);
  const onPreviewRef = useRef(onPreview);
  onPreviewRef.current = onPreview;
  useEffect(
    () => () => {
      if (previewing.current) onPreviewRef.current(null);
    },
    [],
  );

  const preview = (palette: PaletteName | null) => {
    previewing.current = palette !== null;
    onPreview(palette);
  };

  function choose(index: number) {
    const opt = PALETTES[index];
    if (!opt) return;
    buttonsRef.current[index]?.focus();
    if (opt.value !== value) onChoose(opt.value);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number) {
    const last = PALETTES.length - 1;
    let next: number | null = null;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = index === last ? 0 : index + 1;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = index === 0 ? last : index - 1;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = last;
    if (next === null) return;
    e.preventDefault();
    choose(next);
  }

  return (
    <div role="radiogroup" className={styles.grid} {...aria}>
      {PALETTES.map((p, index) => (
        <button
          key={p.value}
          ref={(el) => {
            buttonsRef.current[index] = el;
          }}
          type="button"
          role="radio"
          aria-checked={p.value === value}
          aria-label={p.label}
          aria-description={p.description}
          tabIndex={index === tabStop ? 0 : -1}
          className={styles.card}
          data-palette={p.value}
          data-theme={theme}
          onClick={() => choose(index)}
          onKeyDown={(e) => handleKeyDown(e, index)}
          onMouseEnter={() => preview(p.value)}
          onMouseLeave={() => preview(null)}
        >
          <span className={styles.mini} aria-hidden="true">
            <span className={styles.rail}>
              <i />
              <i className={styles.on} />
              <i />
            </span>
            <span className={styles.side}>
              <b />
              <b className={styles.on} />
              <b />
              <b />
            </span>
            <span className={styles.main}>
              <span className={styles.title} />
              <span className={styles.panel}>
                <span className={styles.line} style={{ width: "70%" }} />
                <span className={styles.line} style={{ width: "45%" }} />
                <span className={styles.actions}>
                  <span className={styles.button} />
                  <span className={`${styles.chip} ${styles.ok}`} />
                  <span className={`${styles.chip} ${styles.warn}`} />
                  <span className={`${styles.chip} ${styles.bad}`} />
                </span>
              </span>
            </span>
          </span>
          <span className={styles.name}>{p.label}</span>
          <span className={styles.description}>{p.description}</span>
        </button>
      ))}
    </div>
  );
}
