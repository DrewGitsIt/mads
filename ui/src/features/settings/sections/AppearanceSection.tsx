import { useEffect, useState } from "react";
import { Chip } from "../../../components/Chip/Chip";
import {
  DEFAULT_CUSTOM_WPM,
  MAX_STREAM_WPM,
  MIN_STREAM_WPM,
  STREAM_WPM_PRESETS,
  clampWpm,
  normalizeStreamSpeed,
} from "../../../conversation/streaming/streamSpeed";
import type { UseConfig } from "../state/useConfig";
import { applyTheme } from "../theme/applyTheme";
import type { PaletteName, StreamSpeed, ThemeMode } from "../../../router/types";
import styles from "./AppearanceSection.module.css";

const PALETTES: PaletteName[] = ["clay", "space", "glacier"];
const THEMES: ThemeMode[] = ["dark", "light", "system"];
const STREAM_SPEEDS: { value: StreamSpeed; label: string }[] = [
  { value: "off", label: "native" },
  { value: "slow", label: "slow" },
  { value: "fast", label: "fast" },
  { value: "custom", label: "custom" },
];

function streamSpeedHint(speed: StreamSpeed, wpm: number): string {
  switch (speed) {
    case "slow":
      return `Reveals replies at ${STREAM_WPM_PRESETS.slow} words per minute.`;
    case "fast":
      return `Reveals replies at ${STREAM_WPM_PRESETS.fast} words per minute.`;
    case "custom":
      return `Reveals replies at ${wpm} words per minute.`;
    default:
      return "Shows replies as fast as the agent sends them.";
  }
}

export interface AppearanceSectionProps {
  config: UseConfig;
}

/** Palette + dark/light/system picker — applies live on click, persists via PUT /config (HANDOFF §5). */
export function AppearanceSection({ config }: AppearanceSectionProps) {
  const { draft, saved, setDraft, save } = config;
  // The custom pace as typed, committed on blur/Enter — saving every
  // keystroke would clamp "3" to the minimum before "350" is finished.
  const [wpmText, setWpmText] = useState("");
  const savedWpm = draft?.stream_wpm;
  useEffect(() => {
    setWpmText(String(clampWpm(savedWpm)));
  }, [savedWpm]);
  if (!draft || !saved) return null;
  const speed = normalizeStreamSpeed(draft.stream_speed);

  const choosePalette = async (palette: PaletteName) => {
    const next = { ...draft, palette };
    setDraft(next);
    applyTheme(next.theme, next.palette);
    const ok = await save(next);
    if (!ok) applyTheme(saved.theme, saved.palette);
  };

  const chooseTheme = async (theme: ThemeMode) => {
    const next = { ...draft, theme };
    setDraft(next);
    applyTheme(next.theme, next.palette);
    const ok = await save(next);
    if (!ok) applyTheme(saved.theme, saved.palette);
  };

  const chooseStreamSpeed = async (stream_speed: StreamSpeed) => {
    const next = { ...draft, stream_speed };
    setDraft(next);
    await save(next);
  };

  const commitWpm = async () => {
    const stream_wpm = clampWpm(Number(wpmText));
    setWpmText(String(stream_wpm));
    if (stream_wpm === draft.stream_wpm) return;
    const next = { ...draft, stream_wpm };
    setDraft(next);
    await save(next);
  };

  return (
    <div className={styles.panel}>
      <section>
        <h3 className={styles.heading}>Palette</h3>
        <div className={styles.options} role="group" aria-label="Palette">
          {PALETTES.map((p) => (
            <Chip
              key={p}
              tone={draft.palette === p ? "info" : "neutral"}
              pressed={draft.palette === p}
              onClick={() => void choosePalette(p)}
            >
              {p}
            </Chip>
          ))}
        </div>
      </section>
      <section>
        <h3 className={styles.heading}>Theme</h3>
        <div className={styles.options} role="group" aria-label="Theme">
          {THEMES.map((t) => (
            <Chip
              key={t}
              tone={draft.theme === t ? "info" : "neutral"}
              pressed={draft.theme === t}
              onClick={() => void chooseTheme(t)}
            >
              {t}
            </Chip>
          ))}
        </div>
      </section>
      <section>
        <h3 className={styles.heading}>Streaming speed</h3>
        <div className={styles.options} role="group" aria-label="Streaming speed">
          {STREAM_SPEEDS.map((s) => (
            <Chip
              key={s.value}
              tone={speed === s.value ? "info" : "neutral"}
              pressed={speed === s.value}
              onClick={() => void chooseStreamSpeed(s.value)}
            >
              {s.label}
            </Chip>
          ))}
        </div>
        {speed === "custom" && (
          <label className={styles.wpm}>
            <input
              type="number"
              className={styles.wpmInput}
              aria-label="Words per minute"
              min={MIN_STREAM_WPM}
              max={MAX_STREAM_WPM}
              step={25}
              placeholder={String(DEFAULT_CUSTOM_WPM)}
              value={wpmText}
              onChange={(e) => setWpmText(e.target.value)}
              onBlur={() => void commitWpm()}
              onKeyDown={(e) => {
                if (e.key === "Enter") void commitWpm();
              }}
            />
            words per minute
          </label>
        )}
        <p className={styles.hint}>
          {streamSpeedHint(speed, clampWpm(draft.stream_wpm))}
          {speed !== "off" &&
            " A long reply can keep revealing after the agent finishes; Show all skips ahead."}
        </p>
      </section>
    </div>
  );
}
