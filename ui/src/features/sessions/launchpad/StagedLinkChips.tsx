import { Chip } from "../../../components/Chip/Chip";
import type { LinkRequest, Session } from "../../../router/types";
import styles from "./StagedLinkChips.module.css";

export interface StagedLinkChipsProps {
  links: LinkRequest[];
  sessions: Session[];
  onRemove: (subjectId: string) => void;
}

/** Same staged-chip-strip pattern as AttachmentChips.tsx, for links staged via "Link to session(s)…". */
export function StagedLinkChips({ links, sessions, onRemove }: StagedLinkChipsProps) {
  if (links.length === 0) return null;

  return (
    <div className={styles.preview}>
      {links.map((link) => {
        const name =
          sessions.find((s) => s.id === link.subject_session_id)?.name || "Untitled session";
        return (
          <Chip key={link.subject_session_id} tone="info" className={styles.chip}>
            <span className={styles.label}>
              {link.preset === "leader" ? `Leader of ${name}` : `Monitoring ${name}`}
            </span>
            <button
              type="button"
              className={styles.remove}
              aria-label={`Remove link to ${name}`}
              onClick={() => onRemove(link.subject_session_id)}
            >
              ✕
            </button>
          </Chip>
        );
      })}
    </div>
  );
}
