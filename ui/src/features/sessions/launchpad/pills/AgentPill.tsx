import { useEffect, useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { router } from "../../../../router/client";
import type { Agent, ResolvedSessionField } from "../../../../router/types";
import pillStyles from "./pills.module.css";

export interface AgentPillProps {
  value?: string;
  resolved?: ResolvedSessionField;
  onChange: (agent: Agent) => void;
  /** Selects capability-inferred staffing (pkg/capinfer + pkg/autostaff) instead of a fixed agent — resolved server-side from the composer's opening prompt when the session is created. */
  onAuto: () => void;
}

export function AgentPill({ value, resolved, onChange, onAuto }: AgentPillProps) {
  const [open, setOpen] = useState(false);
  const [agents, setAgents] = useState<Agent[]>([]);

  useEffect(() => {
    router
      .listAgents()
      .then(setAgents)
      .catch(() => setAgents([]));
  }, []);

  // The staged value is the persona id (agt_…); a workspace default may be
  // either an id or a name. Always show the name the menu shows.
  const displayName = (ref: string) =>
    agents.find((a) => a.id === ref || a.name === ref)?.name ?? ref;
  const isSelected = (agent: Agent) => value === agent.id || value === agent.name;

  const triggerLabel =
    value === "auto"
      ? "Auto"
      : value
        ? displayName(value)
        : resolved?.value
          ? `${displayName(resolved.value)} (default)`
          : "Select persona";

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
          {/* Redesign: the registry record is a persona (prompt +
           * allow/deny tool intents), not an "agent" — the pill's label
           * tracks the data model so the two pills (persona / model) read
           * as the two concepts they set. */}
          <span className={pillStyles.key}>persona</span> {triggerLabel}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        <button
          type="button"
          className={[pillStyles.option, value === "auto" ? pillStyles.optionSelected : ""]
            .filter(Boolean)
            .join(" ")}
          onClick={() => {
            onAuto();
            setOpen(false);
          }}
        >
          Auto
        </button>
        {agents.length === 0 && <div className={pillStyles.groupLabel}>No personas yet</div>}
        {agents.map((agent) => (
          <button
            key={agent.id}
            type="button"
            className={[
              pillStyles.option,
              isSelected(agent) ? pillStyles.optionSelected : "",
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
