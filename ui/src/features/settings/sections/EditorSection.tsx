import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { optionsWith } from "../settingHelpers";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import type { UseConfig } from "../state/useConfig";
import input from "../settingInput.module.css";
import styles from "./EditorSection.module.css";

// keybindings is a plain string in the Go contract (no Valid()), so the
// choices live here; labels are display-only, the stored values lowercase.
const KEYBINDINGS: SegmentedOption[] = [
  { value: "default", label: "Default" },
  { value: "vim", label: "Vim" },
  { value: "emacs", label: "Emacs" },
];

export interface EditorSectionProps {
  config: UseConfig;
}

/** How mads opens files, and the editor keymap. */
export function EditorSection({ config }: EditorSectionProps) {
  const { draft, setDraft } = config;
  if (!draft) return null;

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow
          label="Open files with"
          htmlFor="editor-command"
          help="The command mads runs to open a file. Leave empty to use $EDITOR."
        >
          <input
            id="editor-command"
            type="text"
            spellCheck={false}
            className={`${input.input} ${input.mono}`}
            placeholder="code -g"
            value={draft.editor_command ?? ""}
            onChange={(e) => setDraft({ ...draft, editor_command: e.target.value })}
          />
        </SettingRow>
        <SettingRow label="Keybindings" labelId="keybindings-label">
          <SegmentedControl
            id="keybindings"
            aria-labelledby="keybindings-label"
            value={draft.keybindings}
            options={optionsWith(KEYBINDINGS, draft.keybindings)}
            onChange={(keybindings) => setDraft({ ...draft, keybindings })}
          />
        </SettingRow>
      </SettingsCard>
    </div>
  );
}
