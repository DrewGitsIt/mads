import { useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { RouterValidationError, type RouterClient } from "../../../router/client";
import { PRESET_GRANTS, type LinkPreset, type Session } from "../../../router/types";
import styles from "./LinkChooserDialog.module.css";

export interface LinkChooserDialogProps {
  /** The rail's checked session ids (2+) — becomes leader/subjects once a holder is chosen. */
  sessionIds: string[];
  sessions: Session[];
  routerClient: RouterClient;
  onClose: () => void;
  onCreated: () => void;
}

// Only Monitor and Leader are user-selectable (docs/linked-sessions-mockup.html,
// Drew-reviewed amendment) — Hand-raiser is system-only (design decision 6).
type UserPreset = Extract<LinkPreset, "monitor" | "leader">;

const PRESET_COPY: Record<UserPreset, { title: string; description: string }> = {
  monitor: {
    title: "Monitor",
    description:
      "Follows along: receives a digest after each watched agent's turn. Never speaks into them.",
  },
  leader: {
    title: "Leader",
    description:
      "Gets a tool to message and direct the other agents. Its reach is capped by each session's own trust level.",
  },
};

function sessionName(sessions: Session[], id: string): string {
  return sessions.find((s) => s.id === id)?.name || "Untitled session";
}

/** Monitor/Leader preset chooser + holder picker, per docs/linked-sessions-mockup.html panel 1. */
export function LinkChooserDialog({
  sessionIds,
  sessions,
  routerClient,
  onClose,
  onCreated,
}: LinkChooserDialogProps) {
  const [preset, setPreset] = useState<UserPreset>("leader");
  const [holderId, setHolderId] = useState<string>(sessionIds[0] ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<{ field: string; message: string }[]>([]);
  const [generalError, setGeneralError] = useState<string>();

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  const subjectIds = sessionIds.filter((id) => id !== holderId);
  const roleLabel = preset === "leader" ? "Who leads?" : "Who monitors?";

  async function handleCreate() {
    setSubmitting(true);
    setFieldErrors([]);
    setGeneralError(undefined);
    try {
      for (const subjectId of subjectIds) {
        await routerClient.createLink({
          granting_session_id: holderId,
          subject_session_id: subjectId,
          preset,
        });
      }
      onCreated();
    } catch (e) {
      if (e instanceof RouterValidationError) {
        setFieldErrors(e.errors);
      } else {
        setGeneralError(e instanceof Error ? e.message : "Failed to create links");
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className={styles.backdrop} onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="link-chooser-title"
        className={styles.chooser}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div id="link-chooser-title" className={styles.title}>
          Link sessions
        </div>
        <div className={styles.sub}>
          Pick a mode, then pick which session holds it — every other selected session becomes its
          subject.
        </div>
        <div className={styles.presets}>
          {(Object.keys(PRESET_COPY) as UserPreset[]).map((p) => (
            <button
              key={p}
              type="button"
              className={styles.preset}
              aria-selected={preset === p}
              onClick={() => setPreset(p)}
            >
              <div className={styles.pt}>{PRESET_COPY[p].title}</div>
              <div className={styles.pd}>{PRESET_COPY[p].description}</div>
              <div className={styles.grants}>
                {PRESET_GRANTS[p].map((g) => (
                  <span key={g} className={styles.grant}>
                    {g}
                  </span>
                ))}
              </div>
            </button>
          ))}
        </div>
        <div className={styles.holderPicker}>
          <div className={styles.roleLabel}>{roleLabel}</div>
          {sessionIds.map((id) => {
            const others = sessionIds.filter((otherId) => otherId !== id);
            return (
              <label key={id} className={styles.holderRow}>
                <input
                  type="radio"
                  name="holder"
                  checked={holderId === id}
                  onChange={() => setHolderId(id)}
                />
                <b>{sessionName(sessions, id)}</b>
                <span className={styles.fanOut}>
                  ⟶ {others.map((otherId) => sessionName(sessions, otherId)).join(", ")}
                </span>
              </label>
            );
          })}
        </div>
        <div className={styles.constraint}>
          One leader per subject — a session already led by someone else is refused. Links never
          cross different worktrees of the same repo. Pipeline runs can be monitored, not led.
          (Hand-raiser isn&apos;t offered here: it&apos;s granted automatically to a session&apos;s
          own agent while its pipeline runs, and it talks to systems, not agents.)
        </div>
        {generalError && <p className={styles.error}>{generalError}</p>}
        {fieldErrors.length > 0 && (
          <ul className={styles.fieldErrors}>
            {fieldErrors.map((fe, i) => (
              <li key={i}>
                {fe.field}: {fe.message}
              </li>
            ))}
          </ul>
        )}
        <div className={styles.actions}>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={handleCreate} disabled={submitting}>
            Create {subjectIds.length} link{subjectIds.length === 1 ? "" : "s"}
          </Button>
        </div>
      </div>
    </div>
  );
}
