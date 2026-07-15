import { Panel, PanelBody } from "../../components/Panel/Panel";
import { Chip } from "../../components/Chip/Chip";
import type { MentionCandidate } from "./useMentionResolver";
import styles from "./MentionMenu.module.css";

export interface MentionMenuProps {
  candidates: MentionCandidate[];
  activeIndex: number;
  onSelect: (candidate: MentionCandidate) => void;
}

/** @/ trigger popover — reuses the kit's Panel rather than a bespoke dropdown. */
export function MentionMenu({ candidates, activeIndex, onSelect }: MentionMenuProps) {
  if (candidates.length === 0) return null;

  return (
    <Panel className={styles.menu}>
      <PanelBody>
        <ul className={styles.list} role="listbox">
          {candidates.map((candidate, i) => (
            <li key={candidate.id}>
              <button
                type="button"
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
