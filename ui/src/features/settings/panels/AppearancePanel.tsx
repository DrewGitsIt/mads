import { Chip } from "../../../components/Chip/Chip";
import type { UseConfig } from "../state/useConfig";
import { applyTheme } from "../theme/applyTheme";
import type { PaletteName, ThemeMode } from "../../../router/types";
import styles from "./AppearancePanel.module.css";

const PALETTES: PaletteName[] = ["clay", "space", "glacier"];
const THEMES: ThemeMode[] = ["dark", "light", "system"];

export interface AppearancePanelProps {
  config: UseConfig;
}

/** Palette + dark/light/system picker — applies live on click, persists via PUT /config (HANDOFF §5). */
export function AppearancePanel({ config }: AppearancePanelProps) {
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
    </div>
  );
}
