import { Chip } from "../../components/Chip/Chip";
import type { MentionCandidate } from "./useMentionResolver";
import styles from "./InjectedContentPreview.module.css";

export interface InjectedContentPreviewProps {
  items: MentionCandidate[];
  onRemove: (id: string) => void;
}

/**
 * Preview-only list of staged @/ references — resolved content is not
 * fetched/expanded until send (MADS-UX §2.4: "resolves at send
 * time... before anything reaches the harness").
 */
export function InjectedContentPreview({
  items,
  onRemove,
}: InjectedContentPreviewProps) {
  if (items.length === 0) return null;

  return (
    <div className={styles.preview}>
      {items.map((item) => (
        <Chip key={item.id} tone="neutral">
          {item.label}
          <button
            type="button"
            className={styles.remove}
            aria-label={`Remove ${item.label}`}
            onClick={() => onRemove(item.id)}
          >
            ✕
          </button>
        </Chip>
      ))}
    </div>
  );
}
