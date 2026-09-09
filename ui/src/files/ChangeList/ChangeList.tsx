import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { ChangeKind, FileChange } from "../../router/types";
import { displayPath } from "../displayPath";
import { AttributionChip } from "./AttributionChip";
import { attributionForChange, isOwnActor } from "./attribution";
import styles from "./ChangeList.module.css";

export interface ChangeListProps {
  changes: FileChange[];
  selectedPath?: string;
  onSelect: (path: string) => void;
  /** The viewed session — rows whose actor is this session hide their AttributionChip (own edits are the expected case, not signal). */
  sessionId?: string;
  /** Session.worktree_dir — absolute ledger paths under it render repo-relative (see displayPath). */
  worktreeDir?: string;
}

export const KIND_LABEL: Record<ChangeKind, string> = { added: "A", modified: "M", deleted: "D" };
export const KIND_TONE: Record<ChangeKind, ChipTone> = { added: "ok", modified: "info", deleted: "bad" };

/** FileChange[] rows: A/M/D badge, path, AttributionChip — MADS-UX §3.3. */
export function ChangeList({ changes, selectedPath, onSelect, sessionId, worktreeDir }: ChangeListProps) {
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
            {/* Outer span is the RTL truncation context (ellipsis on the start side, tail/filename preserved); inner span re-isolates the path LTR so characters render in order. Full path stays in title. */}
            <span className={styles.path} title={fc.path}>
              <span className={styles.pathText}>{displayPath(fc.path, worktreeDir)}</span>
            </span>
            {!isOwnActor(fc.actor, sessionId) && (
              <AttributionChip kind={attributionForChange(fc)} actor={fc.actor} />
            )}
          </button>
        </li>
      ))}
    </ul>
  );
}
