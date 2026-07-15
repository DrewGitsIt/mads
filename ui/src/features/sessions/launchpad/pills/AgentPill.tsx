import { useEffect, useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { routerGet } from "../../../../router/client";
import type { Agent } from "../../../../router/types";
import pillStyles from "./pills.module.css";

export interface AgentPillProps {
  value?: string;
  onChange: (agent: Agent) => void;
}

export function AgentPill({ value, onChange }: AgentPillProps) {
  const [open, setOpen] = useState(false);
  const [agents, setAgents] = useState<Agent[]>([]);

  useEffect(() => {
    routerGet<Agent[]>("/agents")
      .then(setAgents)
      .catch(() => setAgents([]));
  }, []);

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
          <span className={pillStyles.key}>agent</span> {value ?? "Select agent"}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        {agents.length === 0 && <div className={pillStyles.groupLabel}>No agents yet</div>}
        {agents.map((agent) => (
          <button
            key={agent.id}
            type="button"
            className={[
              pillStyles.option,
              agent.name === value ? pillStyles.optionSelected : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onClick={() => {
              onChange(agent);
              setOpen(false);
            }}
          >
            {agent.name}
          </button>
        ))}
      </div>
    </Popover>
  );
}
