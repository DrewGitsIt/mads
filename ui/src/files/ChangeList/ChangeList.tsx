import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { ChangeKind, FileChange } from "../../router/types";
import { AttributionChip } from "./AttributionChip";
import { attributionForChange } from "./attribution";
import styles from "./ChangeList.module.css";

export interface ChangeListProps {
  changes: FileChange[];
  selectedPath?: string;
  onSelect: (path: string) => void;
}

export const KIND_LABEL: Record<ChangeKind, string> = { added: "A", modified: "M", deleted: "D" };
export const KIND_TONE: Record<ChangeKind, ChipTone> = { added: "ok", modified: "info", deleted: "bad" };

/** FileChange[] rows: A/M/D badge, path, AttributionChip — MADS-UX §3.3. */
export function ChangeList({ changes, selectedPath, onSelect }: ChangeListProps) {
  if (changes.length === 0) {
    return <div className={styles.empty}>No file changes yet.</div>;
  }

  return (
    <ul className={styles.list}>
      {changes.map((fc) => (
        <li key={fc.id}>
          <button
            type="button"
            className={styles.row}
            aria-current={fc.path === selectedPath}
            data-selected={fc.path === selectedPath}
            onClick={() => onSelect(fc.path)}
          >
            <Chip tone={KIND_TONE[fc.change]} className={styles.kindBadge}>
              {KIND_LABEL[fc.change]}
            </Chip>
            <span className={styles.path}>{fc.path}</span>
            <AttributionChip kind={attributionForChange(fc)} actor={fc.actor} />
          </button>
        </li>
      ))}
    </ul>
  );
}
