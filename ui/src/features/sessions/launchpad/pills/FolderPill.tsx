import { useEffect, useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { PathField } from "../../../../components/PathField/PathField";
import { router } from "../../../../router/client";
import type { ResolvedSessionField, Workspace } from "../../../../router/types";
import pillStyles from "./pills.module.css";

/** Last path segment — the pill is too narrow for a whole path; the full one is its tooltip. */
export function folderName(path: string): string {
  return path.split("/").filter(Boolean).pop() ?? path;
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
}

/**
 * Where the agent starts. Any folder on the machine — typed, or chosen in
 * the native dialog — with recent folders and workspaces as shortcuts. A
 * workspace still brings its own defaults; picking it here starts the
 * session in its primary repo.
 */
export function FolderPill({
  dir,
  workspace,
  resolvedDir,
  resolvedWorkspace,
  recent = [],
  onPickDir,
  onPickWorkspace,
}: FolderPillProps) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);

  useEffect(() => {
    router
      .listWorkspaces()
      .then(setWorkspaces)
      .catch(() => setWorkspaces([]));
  }, []);

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

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={[pillStyles.trigger, dir || workspace ? pillStyles.modified : ""]
            .filter(Boolean)
            .join(" ")}
          title={title}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>folder</span> {label}
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
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              pick(draft);
            }
          }}
        />
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
      </div>
    </Popover>
  );
}
