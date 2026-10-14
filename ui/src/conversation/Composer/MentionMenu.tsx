import { useEffect } from "react";
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

/**
 * @ # / trigger popover — reuses the kit's Panel rather than a bespoke
 * dropdown. A command whose available() said no renders as a disabled row
 * with its reason in place of the description; it can't be clicked, and the
 * Composer's arrow keys skip it.
 */
export function MentionMenu({
  candidates,
  activeIndex,
  onSelect,
}: MentionMenuProps) {
  // Keyboard selection can leave the visible area — keep it in view.
  const activeId = candidates[activeIndex]?.id;
  useEffect(() => {
    if (activeId === undefined) return;
    document
      .getElementById(mentionOptionId(activeId))
      ?.scrollIntoView?.({ block: "nearest" });
  }, [activeId]);

  if (candidates.length === 0) return null;

  // Level-2 candidates (a command's options) all share a `group` — breadcrumb
  // reads "/model ›" so the second level never looks like an unrelated flat list.
  const group = candidates[0]?.group;

  return (
    <Panel className={styles.menu}>
      <PanelBody>
        {group && <div className={styles.breadcrumb}>/{group} ›</div>}
        <ul className={styles.list} role="listbox">
          {candidates.map((candidate, i) => {
            const disabled = candidate.disabledReason !== undefined;
            return (
              <li key={candidate.id}>
                <button
                  type="button"
                  id={mentionOptionId(candidate.id)}
                  role="option"
                  aria-selected={i === activeIndex}
                  aria-disabled={disabled || undefined}
                  disabled={disabled}
                  className={[
                    styles.item,
                    i === activeIndex ? styles.active : "",
                    disabled ? styles.disabled : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  onMouseDown={(e) => {
                    // preventDefault so the textarea doesn't lose focus/selection
                    e.preventDefault();
                    if (!disabled) onSelect(candidate);
                  }}
                >
                  <span className={styles.label}>{candidate.label}</span>
                  {candidate.argHint && (
                    <span className={styles.hint}>{candidate.argHint}</span>
                  )}
                  {candidate.alias && (
                    <span className={styles.hint}>/{candidate.alias}</span>
                  )}
                  {candidate.kind === "skill" &&
                    candidate.healthy === false && (
                      <Chip tone="warn">unhealthy</Chip>
                    )}
                  {disabled ? (
                    <span className={styles.reason}>
                      {candidate.disabledReason}
                    </span>
                  ) : (
                    candidate.detail && (
                      <span className={styles.detail}>{candidate.detail}</span>
                    )
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      </PanelBody>
    </Panel>
  );
}
