import { NumberField } from "../../../components/NumberField/NumberField";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import {
  MAX_STREAM_WPM,
  MIN_STREAM_WPM,
  STREAM_WPM_PRESETS,
  clampWpm,
  normalizeStreamSpeed,
} from "../../../conversation/streaming/streamSpeed";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import type { UseConfig } from "../state/useConfig";
import { applyMotion } from "../theme/applyMotion";
import { applyTheme } from "../theme/applyTheme";
import type { PaletteName, StreamSpeed, ThemeMode } from "../../../router/types";
import styles from "./AppearanceSection.module.css";

// Option labels are display-only; the stored values are the lowercase enums.
const PALETTES: SegmentedOption<PaletteName>[] = [
  { value: "clay", label: "Clay" },
  { value: "space", label: "Space" },
  { value: "glacier", label: "Glacier" },
];
const THEMES: SegmentedOption<ThemeMode>[] = [
  { value: "dark", label: "Dark" },
  { value: "light", label: "Light" },
  { value: "system", label: "System" },
];
// reduce_motion is a boolean; "system" (false) defers to the OS setting.
const REDUCE_MOTION: SegmentedOption<"system" | "on">[] = [
  { value: "system", label: "System" },
  { value: "on", label: "On" },
];
const STREAM_SPEEDS: SegmentedOption<StreamSpeed>[] = [
  { value: "off", label: "As sent" },
  { value: "slow", label: "Slow" },
  { value: "fast", label: "Fast" },
  { value: "custom", label: "Custom" },
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
  const { draft, saved, saveFields } = config;
  if (!draft || !saved) return null;
  const speed = normalizeStreamSpeed(draft.stream_speed);

  // Each picker persists only its own field (saveFields), so a theme click
  // never commits someone's half-finished General edits along with it.
  const choosePalette = async (palette: PaletteName) => {
    applyTheme(draft.theme, palette);
    const ok = await saveFields({ palette });
    if (!ok) applyTheme(saved.theme, saved.palette);
  };

  const chooseTheme = async (theme: ThemeMode) => {
    applyTheme(theme, draft.palette);
    const ok = await saveFields({ theme });
    if (!ok) applyTheme(saved.theme, saved.palette);
  };

  const chooseReduceMotion = async (reduce_motion: boolean) => {
    applyMotion(reduce_motion);
    const ok = await saveFields({ reduce_motion });
    if (!ok) applyMotion(saved.reduce_motion);
  };

  const chooseStreamSpeed = (stream_speed: StreamSpeed) => saveFields({ stream_speed });

  // NumberField commits on blur/Enter only (live={false}) — saving every
  // keystroke would clamp "3" to the minimum before "350" is finished.
  const commitWpm = (stream_wpm: number) => saveFields({ stream_wpm });

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow label="Palette" labelId="palette-label">
          <SegmentedControl
            aria-labelledby="palette-label"
            value={draft.palette}
            options={PALETTES}
            onChange={(p) => void choosePalette(p)}
          />
        </SettingRow>
        <SettingRow label="Theme" labelId="theme-label">
          <SegmentedControl
            aria-labelledby="theme-label"
            value={draft.theme}
            options={THEMES}
            onChange={(t) => void chooseTheme(t)}
          />
        </SettingRow>
        <SettingRow
          label="Reduce motion"
          labelId="reduce-motion-label"
          help={
            <>
              {draft.reduce_motion
                ? "Animations are off: things appear and close instantly."
                : "Follows your system's reduce-motion setting."}{" "}
              Doesn't change streaming speed.
            </>
          }
        >
          <SegmentedControl
            aria-labelledby="reduce-motion-label"
            value={draft.reduce_motion ? "on" : "system"}
            options={REDUCE_MOTION}
            onChange={(v) => void chooseReduceMotion(v === "on")}
          />
        </SettingRow>
        <SettingRow
          label="Streaming speed"
          labelId="stream-speed-label"
          help={
            <>
              {streamSpeedHint(speed, clampWpm(draft.stream_wpm))}
              {speed !== "off" &&
                " A long reply can keep revealing after the agent finishes; Show all skips ahead."}
            </>
          }
        >
          <SegmentedControl
            aria-labelledby="stream-speed-label"
            value={speed}
            options={STREAM_SPEEDS}
            onChange={(v) => void chooseStreamSpeed(v)}
          />
        </SettingRow>
        {speed === "custom" && (
          <SettingRow nested label="Words per minute" htmlFor="stream-wpm">
            <NumberField
              id="stream-wpm"
              live={false}
              min={MIN_STREAM_WPM}
              max={MAX_STREAM_WPM}
              step={25}
              unit="wpm"
              value={clampWpm(draft.stream_wpm)}
              onChange={(n) => void commitWpm(n)}
            />
          </SettingRow>
        )}
      </SettingsCard>
    </div>
  );
}
