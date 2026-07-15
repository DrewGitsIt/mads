import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import styles from "./CredentialEnvVarsField.module.css";

export interface CredentialEnvVarsFieldProps {
  value: string[];
  onChange: (names: string[]) => void;
}

/**
 * Name-list editor for Capability.CredentialEnvVars. Values live only in
 * pkg/credstore, never on this wire type — this component structurally
 * cannot display or transmit anything but the env var names.
 */
export function CredentialEnvVarsField({ value, onChange }: CredentialEnvVarsFieldProps) {
  const [draft, setDraft] = useState("");

  function add() {
    const name = draft.trim();
    if (!name || value.includes(name)) return;
    onChange([...value, name]);
    setDraft("");
  }

  function remove(name: string) {
    onChange(value.filter((n) => n !== name));
  }

  return (
    <div className={styles.field}>
      <span className={styles.label}>Credential env vars</span>
      <div className={styles.chips}>
        {value.map((name) => (
          <Chip key={name} tone="neutral" onClick={() => remove(name)}>
            {name} ×
          </Chip>
        ))}
      </div>
      <div className={styles.add}>
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="e.g. GITHUB_TOKEN"
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <Button variant="secondary" size="sm" onClick={add}>
          Add
        </Button>
      </div>
    </div>
  );
}
