import { Chip } from "../../../components/Chip/Chip";
import type { UseConfig } from "../state/useConfig";
import { applyTheme } from "../theme/applyTheme";
import type { PaletteName, StreamSpeed, ThemeMode } from "../../../router/types";
import styles from "./AppearanceSection.module.css";

const PALETTES: PaletteName[] = ["clay", "space", "glacier"];
const THEMES: ThemeMode[] = ["dark", "light", "system"];
const STREAM_SPEEDS: { value: StreamSpeed; label: string }[] = [
  { value: "off", label: "native" },
  { value: "slow", label: "slow" },
  { value: "medium", label: "medium" },
  { value: "fast", label: "fast" },
];

export interface AppearanceSectionProps {
  config: UseConfig;
}

/** Palette + dark/light/system picker — applies live on click, persists via PUT /config (HANDOFF §5). */
export function AppearanceSection({ config }: AppearanceSectionProps) {
  const { draft, saved, setDraft, save } = config;
  if (!draft || !saved) return null;

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
              tone={draft.stream_speed === s.value ? "info" : "neutral"}
              pressed={draft.stream_speed === s.value}
              onClick={() => void chooseStreamSpeed(s.value)}
            >
              {s.label}
            </Chip>
          ))}
        </div>
      </section>
    </div>
  );
}
