import { useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { NumberField } from "../../../components/NumberField/NumberField";
import { PathField } from "../../../components/PathField/PathField";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { Select } from "../../../components/Select/Select";
import { Switch } from "../../../components/Switch/Switch";
import { router } from "../../../router/client";
import { fastModelAvailable } from "../../../router/fastModel";
import type { Config, DelegateFade, Workspace } from "../../../router/types";
import { ConversationImport } from "../ConversationImport";
import { PreflightChecks } from "../PreflightChecks";
import { RestartBadge } from "../RestartBadge";
import { RESTART_REQUIRED_FIELDS } from "../restartRequired";
import { SettingRow, SettingTag } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { usePreflight } from "../state/usePreflight";
import { useProviderMatrix } from "../state/useProviderMatrix";
import { useFastModelStatus } from "../state/useFastModelStatus";
import type { UseConfig } from "../state/useConfig";
import styles from "./GeneralSection.module.css";

export interface GeneralSectionProps {
  config: UseConfig;
}

// Enum-valued config fields. The Go contract types these as plain strings with
// no Valid() (pkg/contracts/config.go), so the option sets live here — the UI
// is the source of truth for the choices, and DEFAULT_CONFIG's values must be
// members. Density affects list/card spacing; keybindings selects the editor
// keymap. An option list including the current value (see optionsWith) keeps
// an unrecognized persisted value visible instead of silently rewriting it.
// Labels are display-only; the stored values are the lowercase ones.
const DISPLAY_MODES: SegmentedOption[] = [
  { value: "comfortable", label: "Comfortable" },
  { value: "compact", label: "Compact" },
];
const KEYBINDINGS: SegmentedOption[] = [
  { value: "default", label: "Default" },
  { value: "vim", label: "Vim" },
  { value: "emacs", label: "Emacs" },
];
const DELEGATE_FADES: SegmentedOption<DelegateFade>[] = [
  { value: "0m", label: "Right away" },
  { value: "10m", label: "10 min" },
  { value: "never", label: "Never" },
];

/** Option list that always contains `current`, even if it's not a known choice. */
function optionsWith(
  known: SegmentedOption[],
  current: string,
  label: (value: string) => string = (v) => v,
): SegmentedOption[] {
  return current && !known.some((o) => o.value === current)
    ? [{ value: current, label: label(current) }, ...known]
    : known;
}

const restartTag = (field: keyof Config) =>
  RESTART_REQUIRED_FIELDS.has(field) ? <SettingTag tone="warn">Restart</SettingTag> : undefined;

/** Data dir (§0 non-goal — see HANDOFF; not a Config wire field), concurrency limit, log retention, synthesis debounce, editor command, display mode, keybindings, plus the "run preflight" affordance shared with the wizard's PreflightStep. */
export function GeneralSection({ config }: GeneralSectionProps) {
  const { draft, fieldErrors, dirtyRestartFields, setDraft, save } = config;
  const preflight = usePreflight();
  const { providers } = useProviderMatrix();
  const fastModelStatus = useFastModelStatus();
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
  const commitDraftsAvailable = fastModelAvailable(draft, fastModelStatus.status);

  const handleSave = async () => {
    setSaving(true);
    await save();
    setSaving(false);
  };

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow
          label="Concurrency limit"
          htmlFor="concurrency-limit"
          tag={restartTag("concurrency_limit")}
          error={fieldErrors.concurrency_limit}
        >
          <NumberField
            id="concurrency-limit"
            min={1}
            value={draft.concurrency_limit}
            onChange={(n) => setDraft({ ...draft, concurrency_limit: n })}
          />
        </SettingRow>
        <SettingRow
          label="Log retention"
          htmlFor="log-retention-days"
          tag={restartTag("log_retention_days")}
          error={fieldErrors.log_retention_days}
        >
          <NumberField
            id="log-retention-days"
            min={1}
            unit="days"
            value={draft.log_retention_days}
            onChange={(n) => setDraft({ ...draft, log_retention_days: n })}
          />
        </SettingRow>
        <SettingRow
          label="Synthesis debounce"
          htmlFor="synthesis-debounce-ms"
          tag={restartTag("synthesis_debounce_ms")}
          error={fieldErrors.synthesis_debounce_ms}
        >
          <NumberField
            id="synthesis-debounce-ms"
            min={0}
            step={250}
            unit="ms"
            value={draft.synthesis_debounce_ms}
            onChange={(n) => setDraft({ ...draft, synthesis_debounce_ms: n })}
          />
        </SettingRow>
        <SettingRow label="Editor command" htmlFor="editor-command">
          <input
            id="editor-command"
            type="text"
            spellCheck={false}
            className={`${styles.input} ${styles.mono}`}
            value={draft.editor_command ?? ""}
            onChange={(e) => setDraft({ ...draft, editor_command: e.target.value })}
          />
        </SettingRow>
        <SettingRow label="Display mode" labelId="display-mode-label">
          <SegmentedControl
            id="display-mode"
            aria-labelledby="display-mode-label"
            value={draft.display_mode}
            options={optionsWith(DISPLAY_MODES, draft.display_mode)}
            onChange={(display_mode) => setDraft({ ...draft, display_mode })}
          />
        </SettingRow>
        <SettingRow label="Keybindings" labelId="keybindings-label">
          <SegmentedControl
            id="keybindings"
            aria-labelledby="keybindings-label"
            value={draft.keybindings}
            options={optionsWith(KEYBINDINGS, draft.keybindings)}
            onChange={(keybindings) => setDraft({ ...draft, keybindings })}
          />
        </SettingRow>
        <SettingRow label="Default workspace" htmlFor="default-workspace">
          <Select
            id="default-workspace"
            value={draft.default_workspace ?? ""}
            options={[
              { value: "", label: "— none —" },
              ...optionsWith(
                workspaces.map((w) => ({ value: w.id, label: w.name })),
                draft.default_workspace ?? "",
                workspaceLabel,
              ).filter((o) => o.value),
            ]}
            onChange={(ref) => setDraft({ ...draft, default_workspace: ref })}
          />
        </SettingRow>
        {/* Where sessions start when neither the Launchpad nor a workspace
            names a folder — a default workspace's primary repo wins. */}
        <SettingRow label="Default folder" htmlFor="default-dir">
          <div className={styles.fill}>
            <PathField
              id="default-dir"
              className={`${styles.input} ${styles.mono}`}
              dialogTitle="Where should sessions start?"
              placeholder="Where the Router runs"
              value={draft.default_dir ?? ""}
              onChange={(dir) => setDraft({ ...draft, default_dir: dir })}
            />
          </div>
        </SettingRow>
        <SettingRow label="Default provider" htmlFor="default-provider">
          <Select
            id="default-provider"
            value={draft.default_provider ?? ""}
            options={[
              { value: "", label: "— none —" },
              ...optionsWith(
                providers.map((p) => ({ value: p.name, label: p.name })),
                draft.default_provider ?? "",
              ).filter((o) => o.value),
            ]}
            onChange={(name) => setDraft({ ...draft, default_provider: name })}
          />
        </SettingRow>
        {/* Delegates (docs/DELEGATES-DESIGN.md): a session's harness-run
            subagents as nested rows in the session list. Bound to the negation
            of the stored field, so a config predating it shows them. */}
        <SettingRow
          label="Expose subagents in session list"
          htmlFor="expose-delegates"
          help="Subagents a session's agent runs appear under it in the session list while they work, and for a while after they finish. Turn off to keep the list to sessions only — the conversation still shows every subagent."
        >
          <Switch
            id="expose-delegates"
            checked={draft.hide_delegates_in_session_list !== true}
            onChange={(on) => setDraft({ ...draft, hide_delegates_in_session_list: !on })}
          />
        </SettingRow>
        <SettingRow
          nested
          disabled={draft.hide_delegates_in_session_list === true}
          label="Fade out subagent cards after inactivity"
          labelId="delegate-fade-label"
          help="Counted from when a subagent finishes. A running one never fades."
        >
          <SegmentedControl
            id="delegate-fade"
            aria-labelledby="delegate-fade-label"
            value={draft.delegate_fade ?? "10m"}
            options={DELEGATE_FADES}
            disabled={draft.hide_delegates_in_session_list === true}
            onChange={(delegate_fade) => setDraft({ ...draft, delegate_fade })}
          />
        </SettingRow>
        {/* One-click Commit (docs/SESSION-WORKTREES-DESIGN.md D5): Commit… in
            the session dock's status line (and in its ⎇ popover) skips the
            dialog. Off and disabled without a Fast Model to write the
            messages — a saved "on" then just falls back to the dialog. */}
        <SettingRow
          label="Write commit messages for me"
          htmlFor="auto-commit-messages"
          help={
            <>
              Commit in the session's status line skips the dialog: the Fast Model writes the message and commits
              all of this session's files. You can undo from the result. Needs a Fast Model.
              {!commitDraftsAvailable && (
                <div className={styles.unavailable} data-testid="auto-commit-unavailable">
                  {fastModelStatus.loading
                    ? "Checking for a Fast Model…"
                    : "No Fast Model is set up — turn one on under Pipeline & synthesis to use this."}
                </div>
              )}
            </>
          }
        >
          <Switch
            id="auto-commit-messages"
            checked={draft.auto_commit_messages === true}
            disabled={!commitDraftsAvailable && draft.auto_commit_messages !== true}
            onChange={(on) => setDraft({ ...draft, auto_commit_messages: on })}
          />
        </SettingRow>
      </SettingsCard>
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
      <section className={styles.conversations}>
        <h3 className={styles.heading}>Import conversations</h3>
        <ConversationImport />
      </section>
    </div>
  );
}
