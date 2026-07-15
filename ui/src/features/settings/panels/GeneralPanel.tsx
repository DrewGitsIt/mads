import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import { RestartBadge } from "../RestartBadge";
import { usePreflight } from "../state/usePreflight";
import type { UseConfig } from "../state/useConfig";
import styles from "./GeneralPanel.module.css";

export interface GeneralPanelProps {
  config: UseConfig;
}

/** Data dir (§0 non-goal — see HANDOFF; not a Config wire field), concurrency limit, log retention, editor command, display mode, keybindings, plus the "run preflight" affordance shared with the wizard's PreflightStep. */
export function GeneralPanel({ config }: GeneralPanelProps) {
  const { draft, fieldErrors, dirtyRestartFields, setDraft, save } = config;
  const preflight = usePreflight();
  const [saving, setSaving] = useState(false);

  if (!draft) return null;

  const restartVisible = dirtyRestartFields.includes("concurrency_limit") ||
    dirtyRestartFields.includes("log_retention_days");

  const handleSave = async () => {
    setSaving(true);
    await save();
    setSaving(false);
  };

  return (
    <div className={styles.panel}>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="concurrency-limit">
          Concurrency limit
        </label>
        <input
          id="concurrency-limit"
          type="number"
          className={styles.input}
          value={draft.concurrency_limit}
          onChange={(e) => setDraft({ ...draft, concurrency_limit: Number(e.target.value) })}
        />
        {fieldErrors.concurrency_limit && (
          <div className={styles.error}>{fieldErrors.concurrency_limit}</div>
        )}
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="log-retention-days">
          Log retention (days)
        </label>
        <input
          id="log-retention-days"
          type="number"
          className={styles.input}
          value={draft.log_retention_days}
          onChange={(e) => setDraft({ ...draft, log_retention_days: Number(e.target.value) })}
        />
        {fieldErrors.log_retention_days && (
          <div className={styles.error}>{fieldErrors.log_retention_days}</div>
        )}
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="editor-command">
          Editor command
        </label>
        <input
          id="editor-command"
          type="text"
          className={styles.input}
          value={draft.editor_command ?? ""}
          onChange={(e) => setDraft({ ...draft, editor_command: e.target.value })}
        />
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="display-mode">
          Display mode
        </label>
        <input
          id="display-mode"
          type="text"
          className={styles.input}
          value={draft.display_mode}
          onChange={(e) => setDraft({ ...draft, display_mode: e.target.value })}
        />
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="keybindings">
          Keybindings
        </label>
        <input
          id="keybindings"
          type="text"
          className={styles.input}
          value={draft.keybindings}
          onChange={(e) => setDraft({ ...draft, keybindings: e.target.value })}
        />
      </div>
      <div className={styles.actions}>
        <Button variant="primary" onClick={() => void handleSave()} disabled={saving}>
          Save
        </Button>
        <RestartBadge visible={restartVisible} />
      </div>
      <section className={styles.preflight}>
        <h3 className={styles.heading}>Preflight</h3>
        <ul className={styles.checks}>
          {preflight.checks.map((check) => (
            <li key={check.name} className={styles.check}>
              <span aria-hidden="true">{check.healthy ? "✓" : "✗"}</span>
              {check.name}
              {!check.healthy && check.fix_hint && <span className={styles.hint}>{check.fix_hint}</span>}
            </li>
          ))}
        </ul>
        <Button variant="ghost" size="sm" onClick={preflight.rerun} disabled={preflight.loading}>
          Run preflight
        </Button>
      </section>
    </div>
  );
}
