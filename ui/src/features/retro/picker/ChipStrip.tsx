import { Chip } from "../../../components/Chip/Chip";
import type { Session } from "../../../router/types";
import styles from "./ChipStrip.module.css";

export interface ChipStripProps {
  sessionIds: string[];
  sessions: Session[];
}

/** Read-only recap of the sessions a generation run or review covers. */
export function ChipStrip({ sessionIds, sessions }: ChipStripProps) {
  const byId = new Map(sessions.map((session) => [session.id, session]));
  return (
    <div className={styles.strip} aria-label="Sessions in this retro">
      {sessionIds.map((id) => (
        <Chip key={id}>{byId.get(id)?.name ?? id}</Chip>
      ))}
    </div>
  );
}
