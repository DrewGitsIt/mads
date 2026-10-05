import type { Agent } from "./types";

/**
 * Session.agent is a *reference* to a persona, not a name — the launchpad
 * stages the persona id (`agt_…`, see launchpad/pills/AgentPill.tsx) while a
 * workspace default and anything written by hand carry the name, so the
 * contract that holds on both sides is "id-or-name". This is the one place
 * the UI turns one into an Agent, the same split workspaceRef.ts draws for
 * Session.workspace.
 *
 * The rule it exists to enforce: `agt_3212544bb2e37c92` is not something to
 * show a user. The session rail printed exactly that on every card whose
 * persona happened to be stored by id, which is also why a session running
 * the workspace's default persona could still be labelled with it — an id
 * compared against a name never matches, so "differs from the default" was
 * answered wrong as well.
 */
export type AgentIndex = Map<string, Agent>;

export const EMPTY_AGENT_INDEX: AgentIndex = new Map();

/** Indexes an agent list by both of the keys a reference may use. */
export function agentIndex(agents: Agent[]): AgentIndex {
  const index: AgentIndex = new Map();
  // Names first, then ids — should an agent ever be *named* like another's
  // id, the id wins: it is the canonical key, the name is what someone typed.
  for (const agent of agents) if (agent.name) index.set(agent.name, agent);
  for (const agent of agents) if (agent.id) index.set(agent.id, agent);
  return index;
}

/**
 * Whether a reference is an opaque persona id rather than a name. Ids are
 * minted with this prefix (contracts.Agent, `agt_` + hex) and the built-in
 * personas use the readable `agt_system_*` form of it; both are keys, not
 * labels.
 */
export function isAgentId(ref: string): boolean {
  return ref.startsWith("agt_");
}

/**
 * The name to show for a reference, or undefined when there is nothing
 * showable. Deliberately NOT workspaceNameForRef's "fall back to the
 * reference" rule: a workspace reference that fails to resolve is still a
 * name often enough to be worth printing, while an unresolved `agt_…` is a
 * hash and nothing else — a label the user cannot read is worse than no
 * label, and the rail has one line to spend. A reference that is already a
 * name passes through, so a record written by hand still reads.
 */
export function agentNameForRef(
  ref: string | undefined,
  index: AgentIndex,
): string | undefined {
  if (!ref) return undefined;
  const resolved = index.get(ref)?.name;
  if (resolved) return resolved;
  return isAgentId(ref) ? undefined : ref;
}

/** What a persona reference reads as when it can't be resolved to a name. */
export const UNKNOWN_AGENT = "Unknown agent";

/**
 * The label for a persona reference wherever one must be shown even when it
 * doesn't resolve — the conversation header's persona pill, the rail
 * tooltip. Same rule as agentNameForRef, except an unresolvable `agt_…` reads
 * "Unknown agent" instead of disappearing: the slot is there either way, and
 * a hash in it answers nothing. Undefined only when there is no reference.
 */
export function agentLabel(ref: string | undefined, index: AgentIndex): string | undefined {
  if (!ref) return undefined;
  return agentNameForRef(ref, index) ?? UNKNOWN_AGENT;
}
