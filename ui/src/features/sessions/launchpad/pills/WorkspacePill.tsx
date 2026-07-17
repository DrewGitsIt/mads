import { useEffect, useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { router } from "../../../../router/client";
import type { Workspace } from "../../../../router/types";
import pillStyles from "./pills.module.css";

export interface WorkspacePillProps {
  value?: string;
  /** Resolved default workspace name (Config.DefaultWorkspace), shown when nothing is staged yet. */
  resolved?: string;
  onChange: (workspace: Workspace) => void;
}

export function WorkspacePill({ value, resolved, onChange }: WorkspacePillProps) {
  const [open, setOpen] = useState(false);
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);

  useEffect(() => {
    router
      .listWorkspaces()
      .then(setWorkspaces)
      .catch(() => setWorkspaces([]));
  }, []);

  const triggerLabel = value ?? (resolved ? `${resolved} (default)` : "Select workspace");

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
          <span className={pillStyles.key}>ws</span> {triggerLabel}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        {workspaces.length === 0 && <div className={pillStyles.groupLabel}>No workspaces yet</div>}
        {workspaces.map((ws) => (
          <button
            key={ws.id}
            type="button"
            className={[pillStyles.option, ws.name === value ? pillStyles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => {
              onChange(ws);
              setOpen(false);
            }}
          >
            {ws.name}
          </button>
        ))}
      </div>
    </Popover>
  );
}
