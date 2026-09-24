import { useState } from "react";
import { TOOL_INTENTS, type ToolIntent } from "../../router/types";
import styles from "./ToolIntentChips.module.css";

/**
 * ToolIntentChips — the persona editor's tool-allowance surface
 * (agent-concept redesign §6 item 3). A chip over the *closed*
 * ToolIntent vocabulary (read, write, edit, shell, search, fetch, skill,
 * mcp) with an `allow` / `deny` segment on each chip. Because the set is
 * fixed and closed, the user can't accidentally put a harness-specific
 * concrete tool name in here — the field is abstract *by construction*.
 *
 * State model per chip: "allow" | "deny" | "unset". `deny` wins on
 * save (the Go side — the runner's EffectiveToolIntents — also enforces
 * deny-beats-allow, so a record that has both is harmless).
 */
export type ChipState = "allow" | "deny" | "unset";

const DEFAULT_STATES: Record<ToolIntent, ChipState> = {
  read: "unset",
  write: "unset",
  edit: "unset",
  shell: "unset",
  search: "unset",
  fetch: "unset",
  skill: "unset",
  mcp: "unset",
};

function initialStates(allowlist?: ToolIntent[], denylist?: ToolIntent[]): Record<ToolIntent, ChipState> {
  const allow = new Set(allowlist ?? []);
  const deny = new Set(denylist ?? []);
  const out = { ...DEFAULT_STATES };
  for (const i of TOOL_INTENTS) {
    // deny wins on overlap — matches the Go EffectiveToolIntents rule.
    out[i] = deny.has(i) ? "deny" : allow.has(i) ? "allow" : "unset";
  }
  return out;
}

export interface ToolIntentChipsProps {
  allowlist: ToolIntent[];
  denylist: ToolIntent[];
  onChange: (allowlist: ToolIntent[], denylist: ToolIntent[]) => void;
}

const INTENT_LABELS: Record<ToolIntent, string> = {
  read: "Read files",
  write: "Write files",
  edit: "Edit files",
  shell: "Run a shell command",
  search: "Search the codebase",
  fetch: "Network fetch",
  skill: "Invoke a skill",
  mcp: "Invoke an MCP server",
};

export function ToolIntentChips({ allowlist, denylist, onChange }: ToolIntentChipsProps) {
  // Local state (not a derived useMemo) — the chip clicks mutate it, and
  // onChange fires the parent's setter so the record's allowlist/denylist
  // keep up. Initial value comes from the record.
  const [states, setStates] = useState<Record<ToolIntent, ChipState>>(
    () => initialStates(allowlist, denylist),
  );

  function click(intent: ToolIntent) {
    // Three-state cycle: unset -> allow -> deny -> unset.
    const prev = states[intent];
    const next = prev === "unset" ? "allow" : prev === "allow" ? "deny" : "unset";
    const map = { ...states, [intent]: next } as Record<ToolIntent, ChipState>;
    setStates(map);
    emit(map);
  }

  function emit(map: Record<ToolIntent, ChipState>) {
    const allow: ToolIntent[] = [];
    const deny: ToolIntent[] = [];
    for (const i of TOOL_INTENTS) {
      if (map[i] === "allow") allow.push(i);
      else if (map[i] === "deny") deny.push(i);
    }
    onChange(allow, deny);
  }

  return (
    <div className={styles.wrap} role="group" aria-label="Persona tool allowances">
      {TOOL_INTENTS.map((i) => {
        const state = states[i];
        return (
          <button
            key={i}
            type="button"
            className={[styles.chip, `state-${state}`].filter(Boolean).join(" ")}
            onClick={() => click(i)}
            aria-pressed={state === "allow"}
            title={`${INTENT_LABELS[i]} — currently: ${state} (click to cycle allow / deny / unset)`}
          >
            <span className={styles.name}>{i}</span>
            <span className={styles.state}>{state}</span>
          </button>
        );
      })}
    </div>
  );
}
