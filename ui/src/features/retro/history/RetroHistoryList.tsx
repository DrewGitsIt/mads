import { Card } from "../../../components/Card/Card";
import type { Retro } from "../../../router/types";
import styles from "./RetroHistoryList.module.css";

export interface RetroHistoryListProps {
  retros: Retro[];
  onSelect: (retroId: string) => void;
}

/** GET /retros results — click a row to review that retro's DPAs. */
export function RetroHistoryList({ retros, onSelect }: RetroHistoryListProps) {
  if (retros.length === 0) {
    return <div className={styles.empty}>No retros yet.</div>;
  }

  return (
    <ul className={styles.list}>
      {retros.map((retro) => (
        <li key={retro.id}>
          <Card hoverable className={styles.row}>
            <button type="button" className={styles.button} onClick={() => onSelect(retro.id)}>
              <span className={styles.count}>{retro.dpas.length} DPAs</span>
              <span className={styles.date}>{new Date(retro.created_at).toLocaleDateString()}</span>
            </button>
          </Card>
        </li>
      ))}
    </ul>
  );
}
