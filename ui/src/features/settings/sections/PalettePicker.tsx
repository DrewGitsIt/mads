import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Chip } from "../../../components/Chip/Chip";
import { paletteInfo, type PaletteTag } from "../../../tokens/palettes";
import { useAppliedTheme } from "../theme/useAppliedTheme";
import type { PaletteName } from "../../../router/types";
import styles from "./PalettePicker.module.css";

/** Filter chips over the cards: tone first, then vibrancy. */
const FILTERS: { value: PaletteTag | "all"; label: string }[] = [
  { value: "all", label: "All" },
  { value: "warm", label: "Warm" },
  { value: "cool", label: "Cool" },
  { value: "green", label: "Green" },
  { value: "neutral", label: "Neutral" },
  { value: "muted", label: "Muted" },
  { value: "vivid", label: "Vivid" },
  { value: "high-contrast", label: "High contrast" },
];

export interface PalettePickerProps {
  /** The saved palette: the only card that reads checked, hover or not. */
  value: PaletteName;
  onChoose: (palette: PaletteName) => void;
  /** Try a palette on (pointer hover), or null to go back to the saved one. */
  onPreview: (palette: PaletteName | null) => void;
  id?: string;
  "aria-labelledby"?: string;
}

/**
 * Palette choice as preview cards: each draws a tiny app in its own palette
 * (scoped [data-palette]/[data-theme], so tokens.css resolves per card) at
 * the theme currently applied. Pointer hover tries a palette on across the
 * app; keyboard focus doesn't, so tabbing past never flashes the page.
 * Radio-group keys as in SegmentedControl: arrows move focus and choose,
 * among the cards the filter chips leave showing.
 */
export function PalettePicker({ value, onChoose, onPreview, id, ...aria }: PalettePickerProps) {
  const theme = useAppliedTheme();
  const [filter, setFilter] = useState<PaletteTag | "all">("all");
  const shown = filter === "all" ? paletteInfo : paletteInfo.filter((p) => p.tags.includes(filter));
  const buttonsRef = useRef<(HTMLButtonElement | null)[]>([]);
  const selectedIndex = shown.findIndex((p) => p.name === value);
  // The saved palette may be filtered out; then the first card shown takes the tab stop.
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
    const opt = shown[index];
    if (!opt) return;
    buttonsRef.current[index]?.focus();
    if (opt.name !== value) onChoose(opt.name);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number) {
    const last = shown.length - 1;
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
    <div className={styles.picker}>
      <div className={styles.filters} role="group" aria-label="Filter palettes">
        {FILTERS.map((f) => (
          <Chip key={f.value} pressed={filter === f.value} onClick={() => setFilter(f.value)}>
            {f.label}
          </Chip>
        ))}
      </div>
      <div id={id} role="radiogroup" className={styles.grid} {...aria}>
        {shown.map((p, index) => (
          <button
            key={p.name}
            ref={(el) => {
              buttonsRef.current[index] = el;
            }}
            type="button"
            role="radio"
            aria-checked={p.name === value}
            aria-label={p.label}
            aria-description={p.description}
            tabIndex={index === tabStop ? 0 : -1}
            className={styles.card}
            data-palette={p.name}
            data-theme={theme}
            onClick={() => choose(index)}
            onKeyDown={(e) => handleKeyDown(e, index)}
            onMouseEnter={() => preview(p.name)}
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
    </div>
  );
}
