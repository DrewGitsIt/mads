import { useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { RouterValidationError, type RouterClient } from "../../../router/client";
import {
  NEW_SESSION_PLACEHOLDER,
  PRESET_GRANTS,
  type LinkCadence,
  type LinkPreset,
  type LinkRequest,
  type Session,
} from "../../../router/types";
import styles from "./LinkChooserDialog.module.css";

export interface LinkChooserDialogProps {
  /** The rail's checked session ids (2+) — becomes leader/subjects once a holder is chosen. Staged mode passes []. */
  sessionIds: string[];
  sessions: Session[];
  routerClient: RouterClient;
  onClose: () => void;
  onCreated?: () => void;
  /** Launchpad staged mode: phantom "$new" holder, confirm returns requests instead of POSTing. */
  stagedHolder?: {
    label: string; // "This session (not started yet)"
    onStage: (requests: LinkRequest[]) => void;
  };
  /** Staged mode only: seeds when reopening to edit an existing staged selection. */
  initialSubjectIds?: string[];
  initialPreset?: "monitor" | "leader";
  initialCadence?: LinkCadence;
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

const CADENCE_COPY: Record<LinkCadence, { title: string; description: string }> = {
  per_turn: {
    title: "Follow intently",
    description: "Digest after each burst of activity.",
  },
  on_signal: {
    title: "Wake on signals",
    description: "Digest only when it needs attention — a permission request, a crash, a task done or blocked.",
  },
  manual_only: {
    title: "Catch up on request",
    description: "No automatic digests; the agent can ask for a summary.",
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
  stagedHolder,
  initialSubjectIds,
  initialPreset,
  initialCadence,
}: LinkChooserDialogProps) {
  const [preset, setPreset] = useState<UserPreset>(initialPreset ?? "leader");
  const [cadence, setCadence] = useState<LinkCadence>(initialCadence ?? "on_signal");
  const [holderId, setHolderId] = useState<string>(
    stagedHolder ? NEW_SESSION_PLACEHOLDER : (sessionIds[0] ?? ""),
  );
  const [checkedIds, setCheckedIds] = useState<string[]>(initialSubjectIds ?? []);
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

  const subjectIds = stagedHolder ? checkedIds : sessionIds.filter((id) => id !== holderId);
  const roleLabel = preset === "leader" ? "Who leads?" : "Who monitors?";

  // Holder radio is fixed to the phantom row in staged v1 — the other rows keep
  // their radios (disabled, not removed) so new-session-as-subject is a later
  // un-disabling of this same list, not a redesign.
  const holderRows: { id: string; label: string; disabled: boolean }[] = stagedHolder
    ? [
        { id: NEW_SESSION_PLACEHOLDER, label: stagedHolder.label, disabled: false },
        ...checkedIds.map((id) => ({ id, label: sessionName(sessions, id), disabled: true })),
      ]
    : sessionIds.map((id) => ({ id, label: sessionName(sessions, id), disabled: false }));

  function toggleChecked(id: string) {
    setCheckedIds((prev) => (prev.includes(id) ? prev.filter((c) => c !== id) : [...prev, id]));
  }

  async function handleCreate() {
    if (stagedHolder) {
      stagedHolder.onStage(
        checkedIds.map((id) => ({
          granting_session_id: NEW_SESSION_PLACEHOLDER,
          subject_session_id: id,
          preset,
          cadence,
        })),
      );
      return;
    }
    setSubmitting(true);
    setFieldErrors([]);
    setGeneralError(undefined);
    try {
      for (const subjectId of subjectIds) {
        await routerClient.createLink({
          granting_session_id: holderId,
          subject_session_id: subjectId,
          preset,
          cadence,
        });
      }
      onCreated?.();
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
          {stagedHolder
            ? "Pick a mode and the sessions to link — the links are created when the new session starts."
            : "Pick a mode, then pick which session holds it — every other selected session becomes its subject."}
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
        <div className={styles.cadenceSection}>
          <div className={styles.roleLabel}>How often should it hear back?</div>
          <div className={styles.presets}>
            {(Object.keys(CADENCE_COPY) as LinkCadence[]).map((c) => (
              <button
                key={c}
                type="button"
                className={styles.preset}
                aria-selected={cadence === c}
                onClick={() => setCadence(c)}
              >
                <div className={styles.pt}>{CADENCE_COPY[c].title}</div>
                <div className={styles.pd}>{CADENCE_COPY[c].description}</div>
              </button>
            ))}
          </div>
        </div>
        {stagedHolder && (
          <div className={styles.subjectPicker}>
            <div className={styles.roleLabel}>Link to which sessions?</div>
            {sessions.map((s) => (
              <label key={s.id} className={styles.subjectRow}>
                <input
                  type="checkbox"
                  checked={checkedIds.includes(s.id)}
                  onChange={() => toggleChecked(s.id)}
                />
                {s.name || "Untitled session"}
              </label>
            ))}
          </div>
        )}
        <div className={styles.holderPicker}>
          <div className={styles.roleLabel}>{roleLabel}</div>
          {holderRows.map((row) => {
            const others = holderRows.filter((other) => other.id !== row.id);
            return (
              <label
                key={row.id}
                className={[
                  styles.holderRow,
                  row.id === NEW_SESSION_PLACEHOLDER ? styles.phantomRow : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
              >
                <input
                  type="radio"
                  name="holder"
                  checked={holderId === row.id}
                  disabled={row.disabled}
                  onChange={() => setHolderId(row.id)}
                />
                <b>{row.label}</b>
                {others.length > 0 && (
                  <span className={styles.fanOut}>
                    ⟶ {others.map((other) => other.label).join(", ")}
                  </span>
                )}
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
          <Button
            variant="primary"
            onClick={() => void handleCreate()}
            disabled={stagedHolder ? checkedIds.length === 0 : submitting}
          >
            {stagedHolder
              ? `Stage ${checkedIds.length} link${checkedIds.length === 1 ? "" : "s"}`
              : `Create ${subjectIds.length} link${subjectIds.length === 1 ? "" : "s"}`}
          </Button>
        </div>
      </div>
    </div>
  );
}
