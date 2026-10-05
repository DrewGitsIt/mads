import { useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { PathField } from "../../../components/PathField/PathField";
import { router } from "../../../router/client";
import type { Workspace } from "../../../router/types";
import { PreflightChecks } from "../PreflightChecks";
import { RestartBadge } from "../RestartBadge";
import { usePreflight } from "../state/usePreflight";
import { useProviderMatrix } from "../state/useProviderMatrix";
import type { UseConfig } from "../state/useConfig";
import styles from "./GeneralSection.module.css";

export interface GeneralSectionProps {
  config: UseConfig;
}

// Enum-valued config fields. The Go contract types these as plain strings with
// no Valid() (pkg/contracts/config.go), so the option sets live here — the UI
// is the source of truth for the choices, and DEFAULT_CONFIG's values must be
// members. Density affects list/card spacing; keybindings selects the editor
// keymap. An option list including the current value (see selectWith) keeps an
// unrecognized persisted value visible instead of silently rewriting it.
const DISPLAY_MODES = ["comfortable", "compact"];
const KEYBINDINGS = ["default", "vim", "emacs"];

/** Option list that always contains `current`, even if it's not a known choice. */
function selectWith(known: string[], current: string): string[] {
  return current && !known.includes(current) ? [current, ...known] : known;
}

/** Data dir (§0 non-goal — see HANDOFF; not a Config wire field), concurrency limit, log retention, synthesis debounce, editor command, display mode, keybindings, plus the "run preflight" affordance shared with the wizard's PreflightStep. */
export function GeneralSection({ config }: GeneralSectionProps) {
  const { draft, fieldErrors, dirtyRestartFields, setDraft, save } = config;
  const preflight = usePreflight();
  const { providers } = useProviderMatrix();
  const [saving, setSaving] = useState(false);
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);

  useEffect(() => {
    router
      .listWorkspaces()
      .then(setWorkspaces)
      .catch(() => setWorkspaces([]));
  }, []);

  if (!draft) return null;

  // default_workspace may hold an id or a name (the Router accepts either);
  // label by whichever matches, and keep an unknown value visible.
  const workspaceLabel = (ref: string) =>
    workspaces.find((w) => w.id === ref || w.name === ref)?.name ?? ref;

  const restartVisible = dirtyRestartFields.length > 0;

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
        <label className={styles.label} htmlFor="synthesis-debounce-ms">
          Synthesis debounce (ms)
        </label>
        <input
          id="synthesis-debounce-ms"
          type="number"
          className={styles.input}
          value={draft.synthesis_debounce_ms}
          onChange={(e) => setDraft({ ...draft, synthesis_debounce_ms: Number(e.target.value) })}
        />
        {fieldErrors.synthesis_debounce_ms && (
          <div className={styles.error}>{fieldErrors.synthesis_debounce_ms}</div>
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
        <select
          id="display-mode"
          className={styles.input}
          value={draft.display_mode}
          onChange={(e) => setDraft({ ...draft, display_mode: e.target.value })}
        >
          {selectWith(DISPLAY_MODES, draft.display_mode).map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="keybindings">
          Keybindings
        </label>
        <select
          id="keybindings"
          className={styles.input}
          value={draft.keybindings}
          onChange={(e) => setDraft({ ...draft, keybindings: e.target.value })}
        >
          {selectWith(KEYBINDINGS, draft.keybindings).map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="default-workspace">
          Default workspace
        </label>
        <select
          id="default-workspace"
          className={styles.input}
          value={draft.default_workspace ?? ""}
          onChange={(e) => setDraft({ ...draft, default_workspace: e.target.value })}
        >
          <option value="">— none —</option>
          {selectWith(
            workspaces.map((w) => w.id),
            draft.default_workspace ?? "",
          )
            .filter(Boolean)
            .map((ref) => (
              <option key={ref} value={ref}>
                {workspaceLabel(ref)}
              </option>
            ))}
        </select>
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="default-dir">
          Default folder
        </label>
        {/* Where sessions start when neither the Launchpad nor a workspace
            names a folder — a default workspace's primary repo wins. */}
        <PathField
          id="default-dir"
          className={styles.input}
          dialogTitle="Where should sessions start?"
          placeholder="Where the Router runs"
          value={draft.default_dir ?? ""}
          onChange={(dir) => setDraft({ ...draft, default_dir: dir })}
        />
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="default-provider">
          Default provider
        </label>
        <select
          id="default-provider"
          className={styles.input}
          value={draft.default_provider ?? ""}
          onChange={(e) => setDraft({ ...draft, default_provider: e.target.value })}
        >
          <option value="">— none —</option>
          {selectWith(
            providers.map((p) => p.name),
            draft.default_provider ?? "",
          )
            .filter(Boolean)
            .map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
        </select>
      </div>
      <div className={styles.actions}>
        <Button variant="primary" onClick={() => void handleSave()} disabled={saving}>
          Save
        </Button>
        <RestartBadge visible={restartVisible} />
      </div>
      <section className={styles.preflight}>
        <h3 className={styles.heading}>Preflight</h3>
        <div className={styles.checks}>
          <PreflightChecks checks={preflight.checks} loading={preflight.loading} onRerun={preflight.rerun} />
        </div>
        <Button variant="ghost" size="sm" onClick={preflight.rerun} disabled={preflight.loading}>
          Run preflight
        </Button>
      </section>
    </div>
  );
}
