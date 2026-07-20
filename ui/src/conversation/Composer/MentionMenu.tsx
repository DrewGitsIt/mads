import { Panel, PanelBody } from "../../components/Panel/Panel";
import { Chip } from "../../components/Chip/Chip";
import type { MentionCandidate } from "./useMentionResolver";
import styles from "./MentionMenu.module.css";

export interface MentionMenuProps {
  candidates: MentionCandidate[];
  activeIndex: number;
  onSelect: (candidate: MentionCandidate) => void;
}

/** Stable option id shared with the owning textarea's aria-activedescendant. */
export function mentionOptionId(candidateId: string): string {
  return `mention-opt-${candidateId}`;
}

/** @/ trigger popover — reuses the kit's Panel rather than a bespoke dropdown. */
export function MentionMenu({ candidates, activeIndex, onSelect }: MentionMenuProps) {
  if (candidates.length === 0) return null;

  // Level-2 candidates (agent/model) all share a `group` — breadcrumb reads
  // "/agent ›" so the second level never looks like an unrelated flat list.
  const group = candidates[0]?.group;

  return (
    <Panel className={styles.menu}>
      <PanelBody>
        {group && <div className={styles.breadcrumb}>/{group} ›</div>}
        <ul className={styles.list} role="listbox">
          {candidates.map((candidate, i) => (
            <li key={candidate.id}>
              <button
                type="button"
                id={mentionOptionId(candidate.id)}
                role="option"
                aria-selected={i === activeIndex}
                className={[styles.item, i === activeIndex ? styles.active : ""]
                  .filter(Boolean)
                  .join(" ")}
                onMouseDown={(e) => {
                  // preventDefault so the textarea doesn't lose focus/selection
                  e.preventDefault();
                  onSelect(candidate);
                }}
              >
                <span className={styles.label}>{candidate.label}</span>
                {candidate.kind === "command" && candidate.healthy === false && (
                  <Chip tone="warn">unhealthy</Chip>
                )}
                {candidate.detail && <span className={styles.detail}>{candidate.detail}</span>}
              </button>
            </li>
          ))}
        </ul>
      </PanelBody>
    </Panel>
  );
}
