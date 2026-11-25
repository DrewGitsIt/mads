import { useEffect, useId, useState } from "react";
import { Caret } from "../../../components/Caret/Caret";
import { Popover } from "../../../components/Popover/Popover";
import { PathField } from "../../../components/PathField/PathField";
import { Switch } from "../../../components/Switch/Switch";
import { FLAGS } from "../../../featureFlags";
import { useHostClient, useHostScope, useIsRemoteHost } from "../../../router/hostScope";
import { RemoteFolderBrowser } from "./RemoteFolderBrowser";
import type {
  DirStatus,
  ResolvedSessionField,
  Session,
  Workspace,
  WorkspaceRepo,
} from "../../../router/types";
import { LockedPill, type PillLock } from "./LockedPill";
import pillStyles from "./pills.module.css";

/** Last path segment — the pill is too narrow for a whole path; the full one is its tooltip. */
export function folderName(path: string): string {
  return path.split("/").filter(Boolean).pop() ?? path;
}

/** Folders past sessions started in, newest first, each once — the pill's `recent`. */
export function recentDirs(sessions: Session[]): string[] {
  const seen = new Set<string>();
  return [...sessions]
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .map((s) => s.dir)
    .filter((d): d is string => !!d && !seen.has(d) && !!seen.add(d));
}

export interface FolderPillProps {
  /** Staged folder (absolute or "~/…"), if the user picked one. */
  dir?: string;
  /** Staged workspace name, if the user picked a workspace shortcut instead. */
  workspace?: string;
  /** Where the session would start with nothing staged (GET /sessions/defaults). */
  resolvedDir?: ResolvedSessionField;
  /** Workspace whose defaults resolved, if any — named in place of its repo path. */
  resolvedWorkspace?: string;
  /** Folders past sessions started in, most recent first. */
  recent?: string[];
  onPickDir: (dir: string) => void;
  onPickWorkspace: (workspace: Workspace) => void;
  /** Staged isolation ("worktree" | "current_dir"), if the user flipped the switch. */
  isolation?: string;
  /** Isolation with nothing staged (GET /sessions/defaults). */
  resolvedIsolation?: ResolvedSessionField;
  /** Omitted: the pill is folder-only — no isolation part, no switch. */
  onIsolationChange?: (isolation: string) => void;
  /** What's at the resolved folder: a plain folder can't have a worktree, a missing one has neither. */
  dirStatus?: DirStatus;
  /** Isolation fixed by context (a link joining another session's worktree); the folder stays choosable. */
  isolationLocked?: PillLock;
  /** A multi-repo workspace's repos — with FLAGS.multiRepo, the worktree switch lists them. */
  repos?: WorkspaceRepo[];
  selectedRepoPaths?: string[];
  onRepoPathsChange?: (paths: string[]) => void;
  /** Fixed by context (e.g. a link): renders the read-only LockedPill with the reason as its tooltip. */
  locked?: PillLock;
  /** Overrides the "staged override" accent; default: a staged folder, workspace or isolation. The /new card passes "differs from this session". */
  modified?: boolean;
}

/**
 * Where the session works inside the folder, as the pill shows it at rest.
 * A plain folder forces in place (there's no repo to branch); a missing one
 * has no answer yet. `forced` carries the reason for the tooltip.
 */
function isolationShown(
  isolation: string | undefined,
  dirStatus: DirStatus | undefined,
  folder: string | undefined,
): { value?: string; forced?: string } {
  if (dirStatus === "missing") return {};
  if (dirStatus === "plain") {
    return {
      value: "current_dir",
      forced: `${folder ?? "This folder"} isn't a git repository, so there's no worktree to isolate this session in.`,
    };
  }
  return { value: isolation };
}

function isolationLabel(value: string | undefined): string {
  if (value === "worktree") return "⎇ worktree";
  if (value === "current_dir") return "▭ in place";
  return "—";
}

/**
 * Where the agent starts. Any folder on the machine — typed, or chosen in
 * the native dialog — with recent folders and workspaces as shortcuts. A
 * workspace still brings its own defaults; picking it here starts the
 * session in its primary repo.
 *
 * With onIsolationChange it also says *how* the session works there: the
 * pill reads "mads (default) │ ⎇ worktree", so the default folder's
 * isolation shows without opening anything, and the menu's switch changes
 * it. A plain folder forces in place (switch disabled, reason on hover) —
 * the case that used to be a warning under the pills.
 */
export function FolderPill({
  dir,
  workspace,
  resolvedDir,
  resolvedWorkspace,
  recent = [],
  onPickDir,
  onPickWorkspace,
  isolation,
  resolvedIsolation,
  onIsolationChange,
  dirStatus,
  isolationLocked,
  repos,
  selectedRepoPaths,
  onRepoPathsChange,
  locked,
  modified,
}: FolderPillProps) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const switchId = useId();
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  // Mesh mode: starting on another device lists that device's workspaces
  // and browses its folders (docs/MESH-MODE-DESIGN.md D6).
  const client = useHostClient();
  const host = useHostScope();
  const remote = useIsRemoteHost();

  useEffect(() => {
    client
      .listWorkspaces()
      .then(setWorkspaces)
      .catch(() => setWorkspaces([]));
  }, [client]);

  function pick(path: string) {
    const trimmed = path.trim();
    if (!trimmed) return;
    onPickDir(trimmed);
    setDraft("");
    setOpen(false);
  }

  let label: string;
  let title: string | undefined;
  if (dir) {
    label = folderName(dir);
    title = dir;
  } else if (workspace) {
    label = workspace;
  } else if (resolvedWorkspace) {
    label = `${resolvedWorkspace} (default)`;
    title = resolvedDir?.value;
  } else if (resolvedDir?.value) {
    label = `${folderName(resolvedDir.value)} (default)`;
    title = resolvedDir.value;
  } else {
    label = "Choose folder";
  }

  const recentShown = recent.filter((r) => r !== dir).slice(0, 5);

  const withIsolation = onIsolationChange !== undefined;
  const iso = isolationShown(isolation ?? resolvedIsolation?.value, dirStatus, folderName(dir ?? resolvedDir?.value ?? "") || undefined);
  const worktree = iso.value === "worktree";
  const showRepos = FLAGS.multiRepo && worktree && !!repos && repos.length > 1;
  const repoCount = showRepos
    ? repos!.filter((r) => r.primary || (selectedRepoPaths ?? []).includes(r.path)).length
    : 0;
  const isoText = isolationLocked?.label ?? `${isolationLabel(iso.value)}${showRepos ? ` · ${repoCount} repos` : ""}`;
  const isoPart = withIsolation && (
    <span
      className={[pillStyles.isolation, iso.forced || isolationLocked ? pillStyles.isolationMuted : ""]
        .filter(Boolean)
        .join(" ")}
      title={isolationLocked?.reason ?? iso.forced}
      data-testid="folder-isolation"
    >
      {isoText}
    </span>
  );
  const staged = Boolean(dir || workspace) || isolation !== undefined;

  if (locked) {
    return <LockedPill name="folder" label={withIsolation ? `${label} · ${isoText}` : label} lock={locked} />;
  }

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={[pillStyles.trigger, (modified ?? staged) ? pillStyles.modified : ""]
            .filter(Boolean)
            .join(" ")}
          title={title}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>folder</span> {label}
          {isoPart}
          <Caret />
        </button>
      }
    >
      <div className={[pillStyles.menu, pillStyles.folderMenu].join(" ")}>
        <PathField
          aria-label="Folder path"
          className={pillStyles.pathInput}
          placeholder="Type a path, then ↵"
          dialogTitle="Where should the agent start?"
          value={draft}
          onChange={setDraft}
          onPick={pick}
          noNativePicker={remote}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              pick(draft);
            }
          }}
        />
        {remote && host && <RemoteFolderBrowser peer={host.name} onPick={pick} />}
        {recentShown.length > 0 && (
          <>
            <div className={pillStyles.groupLabel}>Recent</div>
            {recentShown.map((path) => (
              <button
                key={path}
                type="button"
                className={pillStyles.option}
                title={path}
                onClick={() => pick(path)}
              >
                <span className={pillStyles.optionLabel}>{folderName(path)}</span>
                <span className={pillStyles.optionDescription}>{path}</span>
              </button>
            ))}
          </>
        )}
        {workspaces.length > 0 && (
          <>
            <div className={pillStyles.groupLabel}>Workspaces</div>
            {workspaces.map((ws) => {
              const primary = ws.repos?.find((r) => r.primary)?.path;
              return (
                <button
                  key={ws.id}
                  type="button"
                  className={[
                    pillStyles.option,
                    !dir && ws.name === workspace ? pillStyles.optionSelected : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  onClick={() => {
                    onPickWorkspace(ws);
                    setOpen(false);
                  }}
                >
                  <span className={pillStyles.optionLabel}>{ws.name}</span>
                  {primary && <span className={pillStyles.optionDescription}>{primary}</span>}
                </button>
              );
            })}
          </>
        )}
        {withIsolation && !isolationLocked && dirStatus !== "missing" && (
          <div className={pillStyles.isolationRow}>
            <Switch
              id={switchId}
              checked={worktree}
              disabled={!!iso.forced}
              onChange={(on) => onIsolationChange!(on ? "worktree" : "current_dir")}
            />
            <label htmlFor={switchId} className={pillStyles.isolationText}>
              <span className={pillStyles.optionLabel}>Work in a new worktree</span>
              <span className={pillStyles.optionDescription}>
                {iso.forced ?? "Its own branch; Land merges it back."}
              </span>
            </label>
          </div>
        )}
        {showRepos && (
          <div className={pillStyles.repoChecklist}>
            {repos!.map((repo) => (
              <label key={repo.path} className={pillStyles.repoItem}>
                <input
                  type="checkbox"
                  disabled={repo.primary}
                  checked={repo.primary || (selectedRepoPaths ?? []).includes(repo.path)}
                  onChange={(e) => {
                    const current = new Set(selectedRepoPaths ?? []);
                    if (e.target.checked) current.add(repo.path);
                    else current.delete(repo.path);
                    onRepoPathsChange?.([...current]);
                  }}
                />{" "}
                {repo.path}
                {repo.primary && " (primary)"}
              </label>
            ))}
          </div>
        )}
      </div>
    </Popover>
  );
}
