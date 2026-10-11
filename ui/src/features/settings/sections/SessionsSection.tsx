import { useEffect, useState } from "react";
import { PathField } from "../../../components/PathField/PathField";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { Select } from "../../../components/Select/Select";
import { Switch } from "../../../components/Switch/Switch";
import { displayPath } from "../../../files/displayPath";
import { router } from "../../../router/client";
import { fastModelAvailable } from "../../../router/fastModel";
import type { Config, DelegateFade, Workspace } from "../../../router/types";
import type { SectionId } from "../sections";
import { optionsWith } from "../settingHelpers";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { InlineLink, StatusLine } from "../StatusLine";
import { useFastModelStatus } from "../state/useFastModelStatus";
import type { UseConfig } from "../state/useConfig";
import input from "../settingInput.module.css";
import styles from "./SessionsSection.module.css";

const DELEGATE_FADES: SegmentedOption<DelegateFade>[] = [
  { value: "0m", label: "Right away" },
  { value: "10m", label: "10 min" },
  { value: "never", label: "Never" },
];

export type StartIn = "workspace" | "folder" | "default";

const START_IN: SegmentedOption<StartIn>[] = [
  { value: "workspace", label: "Workspace" },
  { value: "folder", label: "Folder" },
  { value: "default", label: "mads default" },
];

/**
 * Where a new session starts, as the saved config has it. A config holding
 * both (from before the two were exclusive) reads as Workspace — the
 * Router's precedence too, where a workspace's primary repo wins.
 */
export function deriveStartIn(cfg: Config): StartIn {
  if (cfg.default_workspace) return "workspace";
  if (cfg.default_dir) return "folder";
  return "default";
}

export interface SessionsSectionProps {
  config: UseConfig;
  /** Scrolls the page to another section ("Set one up →" goes to Agents & models). */
  onNavigate: (id: SectionId) => void;
  /** Changes when the draft is reset (Discard, Save, profile switch), dropping an unconfirmed Start in choice. */
  resetKey?: number;
}

/** What a session starts with: where it starts, how its subagents show, one-click commits. */
export function SessionsSection({ config, onNavigate, resetKey }: SessionsSectionProps) {
  const { draft, setDraft } = config;
  const fastModelStatus = useFastModelStatus();

  if (!draft) return null;

  const commitDraftsAvailable = fastModelAvailable(draft, fastModelStatus.status);
  const delegatesHidden = draft.hide_delegates_in_session_list === true;

  return (
    <div className={styles.panel}>
      <StartInCard key={resetKey} draft={draft} setDraft={setDraft} />

      {/* Delegates (docs/DELEGATES-DESIGN.md): a session's harness-run
          subagents as nested rows in the session list. Bound to the negation
          of the stored field, so a config predating it shows them. */}
      <SettingsCard title="Subagents">
        <SettingRow
          label="Show subagents in the session list"
          htmlFor="expose-delegates"
          help="They appear under their session while they work. The conversation always shows them."
        >
          <Switch
            id="expose-delegates"
            checked={!delegatesHidden}
            onChange={(on) => setDraft({ ...draft, hide_delegates_in_session_list: !on })}
          />
        </SettingRow>
        <SettingRow
          nested
          disabled={delegatesHidden}
          label="Hide finished ones after"
          labelId="delegate-fade-label"
          help="Counted from when a subagent finishes. A running one never fades."
        >
          <SegmentedControl
            id="delegate-fade"
            aria-labelledby="delegate-fade-label"
            value={draft.delegate_fade ?? "10m"}
            options={DELEGATE_FADES}
            disabled={delegatesHidden}
            onChange={(delegate_fade) => setDraft({ ...draft, delegate_fade })}
          />
        </SettingRow>
      </SettingsCard>

      {/* One-click Commit (docs/SESSION-WORKTREES-DESIGN.md D5): Commit… in
          the session dock's status line (and in its ⎇ popover) skips the
          dialog. Off and disabled without a Fast Model to write the
          messages — a saved "on" then just falls back to the dialog. */}
      <SettingsCard title="Commits">
        <SettingRow
          label="Write commit messages for me"
          htmlFor="auto-commit-messages"
          help="Commit in the session's status line skips the dialog: the Fast Model writes the message and commits all of this session's files. You can undo from the result."
        >
          <Switch
            id="auto-commit-messages"
            checked={draft.auto_commit_messages === true}
            disabled={!commitDraftsAvailable && draft.auto_commit_messages !== true}
            onChange={(on) => setDraft({ ...draft, auto_commit_messages: on })}
          />
        </SettingRow>
        {!commitDraftsAvailable &&
          (fastModelStatus.loading ? (
            <StatusLine tone="muted" testId="auto-commit-unavailable">
              Checking for a Fast Model…
            </StatusLine>
          ) : (
            <StatusLine tone="warn" testId="auto-commit-unavailable">
              Needs a Fast Model, and none is working right now.{" "}
              <InlineLink onClick={() => onNavigate("models")}>Set one up →</InlineLink>
            </StatusLine>
          ))}
      </SettingsCard>
    </div>
  );
}

interface StartInCardProps {
  draft: Config;
  setDraft: (next: Config) => void;
}

/**
 * Start in: one choice over default_workspace / default_dir, exclusive the
 * way the Launchpad's withStagedDefaults makes them — a workspace clears the
 * folder and vice versa, mads default clears both.
 *
 * Picking Workspace or Folder when that field already holds a value applies
 * at once (clearing the other). Picking one that's still empty only opens
 * its picker: nothing changes, and nothing counts as unsaved, until a value
 * is chosen — so the save bar never offers to save a half-made choice. The
 * picked-but-empty segment is view state, dropped when the card remounts
 * (resetKey).
 */
function StartInCard({ draft, setDraft }: StartInCardProps) {
  const [chosen, setChosen] = useState<StartIn | null>(null);
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);

  useEffect(() => {
    router
      .listWorkspaces()
      .then(setWorkspaces)
      .catch(() => setWorkspaces([]));
  }, []);

  const mode = chosen ?? deriveStartIn(draft);

  const chooseMode = (next: StartIn) => {
    setChosen(next);
    if (next === "default") {
      if (draft.default_workspace || draft.default_dir) {
        setDraft({ ...draft, default_workspace: "", default_dir: "" });
      }
    } else if (next === "workspace" && draft.default_workspace && draft.default_dir) {
      setDraft({ ...draft, default_dir: "" });
    } else if (next === "folder" && draft.default_dir && draft.default_workspace) {
      setDraft({ ...draft, default_workspace: "" });
    }
  };

  // default_workspace may hold an id or a name (the Router accepts either);
  // label by whichever matches, and keep an unknown value visible.
  const workspaceLabel = (ref: string) =>
    workspaces.find((w) => w.id === ref || w.name === ref)?.name ?? ref;
  const workspaceRef = mode === "workspace" ? (draft.default_workspace ?? "") : "";

  return (
    <SettingsCard title="Where sessions start">
      <SettingRow
        label="Start in"
        labelId="start-in-label"
        help="A workspace starts in its primary repo. Choosing one clears the other, the same way the Launchpad does."
      >
        <SegmentedControl
          id="start-in"
          aria-labelledby="start-in-label"
          value={mode}
          options={START_IN}
          onChange={chooseMode}
        />
      </SettingRow>

      {mode === "workspace" && (
        <SettingRow
          nested
          label="Workspace"
          htmlFor="default-workspace"
          help={workspaces.length === 0 && !workspaceRef ? "No workspaces yet. Create one in the Library." : undefined}
        >
          <Select
            id="default-workspace"
            value={workspaceRef}
            placeholder="Choose a workspace"
            options={optionsWith(
              workspaces.map((w) => ({ value: w.id, label: w.name })),
              workspaceRef,
              workspaceLabel,
            )}
            onChange={(ref) => setDraft({ ...draft, default_workspace: ref, default_dir: "" })}
          />
        </SettingRow>
      )}

      {mode === "folder" && (
        <SettingRow nested label="Folder" htmlFor="default-dir">
          <div className={input.fill}>
            <PathField
              id="default-dir"
              className={`${input.input} ${input.mono}`}
              dialogTitle="Where should sessions start?"
              placeholder="Choose a folder"
              value={draft.default_dir ?? ""}
              onChange={(dir) => setDraft({ ...draft, default_dir: dir, default_workspace: "" })}
            />
          </div>
        </SettingRow>
      )}

      {mode === "default" && (
        <SettingRow nested stacked>
          <p className={styles.fallback} data-testid="start-in-fallback">
            {draft.fallback_dir ? (
              <>
                Sessions start in <code className={styles.path}>{displayPath(draft.fallback_dir)}</code>, the folder
                mads was launched from.
              </>
            ) : (
              <>Sessions start in the folder mads was launched from.</>
            )}{" "}
            Choose <b>Folder</b> to pin a different one. The Launchpad can always pick a folder for a single
            session.
          </p>
        </SettingRow>
      )}
    </SettingsCard>
  );
}
