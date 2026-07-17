import { useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { FLAGS } from "../../../../featureFlags";
import type { ResolvedSessionField, WorkspaceRepo } from "../../../../router/types";
import pillStyles from "./pills.module.css";

const ISOLATION_OPTIONS: { value: string; label: string }[] = [
  { value: "worktree", label: "New worktree" },
  { value: "current_dir", label: "Current directory" },
];

export interface IsolationPillProps {
  value?: string;
  resolved?: ResolvedSessionField;
  onChange: (isolation: string) => void;
  repos?: WorkspaceRepo[];
  selectedRepoPaths?: string[];
  onRepoPathsChange?: (paths: string[]) => void;
}

export function IsolationPill({
  value,
  resolved,
  onChange,
  repos,
  selectedRepoPaths,
  onRepoPathsChange,
}: IsolationPillProps) {
  const [open, setOpen] = useState(false);
  const selected = ISOLATION_OPTIONS.find((o) => o.value === value);
  const resolvedOption = ISOLATION_OPTIONS.find((o) => o.value === resolved?.value);
  const showChecklist = FLAGS.multiRepo && value === "worktree" && repos && repos.length > 1;

  const triggerLabel =
    selected?.label ?? (resolvedOption ? `${resolvedOption.label} (default)` : "Select isolation");

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={pillStyles.trigger}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>isolation</span> {triggerLabel}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        {ISOLATION_OPTIONS.map((o) => (
          <button
            key={o.value}
            type="button"
            className={[pillStyles.option, o.value === value ? pillStyles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => onChange(o.value)}
          >
            {o.label}
          </button>
        ))}
        {showChecklist && (
          <>
            <div className={pillStyles.groupLabel}>Repos</div>
            {repos!.map((repo) => (
              <label key={repo.path} className={pillStyles.option}>
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
          </>
        )}
      </div>
    </Popover>
  );
}
