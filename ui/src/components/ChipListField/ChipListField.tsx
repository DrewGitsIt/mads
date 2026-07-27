import { useState } from "react";
import { Button } from "../Button/Button";
import { Chip } from "../Chip/Chip";
import styles from "./ChipListField.module.css";

export interface ChipListFieldProps {
  label: string;
  value: string[];
  onChange: (values: string[]) => void;
  placeholder?: string;
  id?: string;
}

/** Free-text chip-list editor — for plain string lists that aren't backed by a registry. */
export function ChipListField({ label, value, onChange, placeholder, id }: ChipListFieldProps) {
  const [draft, setDraft] = useState("");

  function add() {
    const item = draft.trim();
    if (!item || value.includes(item)) return;
    onChange([...value, item]);
    setDraft("");
  }

  function remove(item: string) {
    onChange(value.filter((v) => v !== item));
  }

  return (
    <div className={styles.field}>
      <span className={styles.label} id={id ? `${id}-label` : undefined}>
        {label}
      </span>
      {value.length > 0 && (
        <div className={styles.chips}>
          {value.map((item) => (
            <Chip key={item} tone="neutral" onClick={() => remove(item)}>
              {item} ×
            </Chip>
          ))}
        </div>
      )}
      <div className={styles.add}>
        <input
          id={id}
          className={styles.input}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={placeholder}
          aria-labelledby={id ? `${id}-label` : undefined}
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
