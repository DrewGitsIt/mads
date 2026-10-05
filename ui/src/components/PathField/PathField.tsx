import type { InputHTMLAttributes } from "react";
import { Button } from "../Button/Button";
import { getDirectoryPicker } from "../../platform/directoryPicker";
import styles from "./PathField.module.css";

export interface PathFieldProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange"> {
  value: string;
  onChange: (path: string) => void;
  /** Title of the native folder dialog, e.g. "Choose a repo". */
  dialogTitle?: string;
  /**
   * Called with a folder chosen in the native dialog, instead of onChange —
   * for fields where choosing is the whole act (RepoList adds the repo
   * immediately rather than leaving it in the draft).
   */
  onPick?: (path: string) => void;
}

/**
 * A directory path input with a "Browse…" button that opens the native
 * folder chooser. The text input stays: it's how a path gets in outside the
 * desktop app (a browser can't return absolute paths), and typing a known
 * path is often faster than clicking to it. The button only renders when the
 * Electron bridge is present.
 */
export function PathField({
  value,
  onChange,
  dialogTitle,
  onPick,
  className,
  ...inputProps
}: PathFieldProps) {
  const picker = getDirectoryPicker();

  async function browse() {
    const chosen = await picker?.pickDirectory({
      title: dialogTitle,
      defaultPath: value.trim() || undefined,
    });
    if (chosen) (onPick ?? onChange)(chosen);
  }

  return (
    <div className={styles.field}>
      <input
        type="text"
        spellCheck={false}
        {...inputProps}
        className={[styles.input, className].filter(Boolean).join(" ")}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      {picker && (
        <Button type="button" variant="secondary" size="sm" onClick={() => void browse()}>
          Browse…
        </Button>
      )}
    </div>
  );
}
