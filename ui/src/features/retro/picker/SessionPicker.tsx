import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Chip } from "../../../components/Chip/Chip";
import { filterByKind, type KindFilter } from "./sessionKind";
import type { Session } from "../../../router/types";
import styles from "./SessionPicker.module.css";

export interface SessionPickerProps {
  sessions: Session[];
  selectedIds: string[];
  onToggle: (id: string) => void;
  onClear: () => void;
}

const KIND_FILTERS: Array<{ id: KindFilter; label: string }> = [
  { id: "all", label: "All" },
  { id: "work", label: "Work" },
  { id: "retro-generated", label: "Retro-generated" },
];

/** Checkbox session picker with kind filters (All/Work/Retro-generated) and a clear-selection action. */
export function SessionPicker({ sessions, selectedIds, onToggle, onClear }: SessionPickerProps) {
  const [kindFilter, setKindFilter] = useState<KindFilter>("all");
  const filtered = filterByKind(sessions, kindFilter);

  return (
    <div className={styles.picker}>
      <div className={styles.filters} role="tablist" aria-label="Filter sessions by kind">
        {KIND_FILTERS.map((filter) => (
          <Chip
            key={filter.id}
            tone={kindFilter === filter.id ? "info" : "neutral"}
            pressed={kindFilter === filter.id}
            onClick={() => setKindFilter(filter.id)}
          >
            {filter.label}
          </Chip>
        ))}
      </div>
      <ul className={styles.list}>
        {filtered.map((session) => (
          <li key={session.id} className={styles.row}>
            <label className={styles.label}>
              <input
                type="checkbox"
                checked={selectedIds.includes(session.id)}
                onChange={() => onToggle(session.id)}
              />
              {session.name}
            </label>
          </li>
        ))}
      </ul>
      <Button variant="ghost" size="sm" onClick={onClear} disabled={selectedIds.length === 0}>
        Clear selection
      </Button>
    </div>
  );
}
