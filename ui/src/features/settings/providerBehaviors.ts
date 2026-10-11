import type { BehaviorInfo, ProviderInfo, ProviderSupport } from "../../router/types";

// Readable names for the capability matrix's behaviour keys
// (providers/provider/matrix.go). A key the Router adds later falls back to
// its humanized form rather than showing raw snake_case.
const BEHAVIOR_LABELS: Record<string, string> = {
  interactive: "Interactive sessions",
  interrupt: "Interrupt",
  one_shot: "One-shot",
  resume: "Resume",
  transcript: "Transcript",
  model_discovery: "Model discovery",
  cost_reporting: "Cost reporting",
  acp: "ACP events",
  pty: "Terminal mode",
  branching: "Branching",
  cwd_reroot: "Folder reroot",
  mcp_servers: "MCP servers",
  trust_limited: "Limited trust",
  trust_read_only: "Read-only trust",
  trust_auto_review: "Auto-review trust",
  one_shot_stream: "One-shot streaming",
  ref_announce: "Early session ref",
  delegate_lifecycle: "Subagent cards",
  delegate_transcript: "Subagent transcripts",
  delegate_steer: "Subagent messages",
  delegate_stop: "Stopping one subagent",
};

export function behaviorLabel(behavior: string): string {
  const known = BEHAVIOR_LABELS[behavior];
  if (known) return known;
  const words = behavior.replaceAll("_", " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export const SUPPORT_LABELS: Record<ProviderSupport, string> = {
  native: "Native",
  emulated: "Emulated",
  unsupported: "No",
};

export const SUPPORT_TONES: Record<ProviderSupport, "ok" | "warn" | "bad"> = {
  native: "ok",
  emulated: "warn",
  unsupported: "bad",
};

// "No branching", but "No MCP servers": lower-case the first letter unless
// the label opens with an acronym.
function lowerFirst(label: string): string {
  return /^[A-Z]{2}/.test(label) ? label : label.charAt(0).toLowerCase() + label.slice(1);
}

export interface CapabilityTag {
  behavior: string;
  tone: "warn" | "bad";
  label: string;
}

/**
 * What `provider` doesn't do natively, in the matrix's behaviour order:
 * unsupported first-class as "No …", emulated as "… emulated". Native
 * behaviours — and ones the provider declares nothing for — say nothing.
 */
export function capabilityTags(provider: ProviderInfo, behaviors: BehaviorInfo[]): CapabilityTag[] {
  const tags: CapabilityTag[] = [];
  for (const { behavior } of behaviors) {
    const support = provider.matrix[behavior];
    if (support === "unsupported") {
      tags.push({ behavior, tone: "bad", label: `No ${lowerFirst(behaviorLabel(behavior))}` });
    } else if (support === "emulated") {
      tags.push({ behavior, tone: "warn", label: `${behaviorLabel(behavior)} emulated` });
    }
  }
  return tags;
}

/**
 * The tags that fit under the provider picker: at most `limit`, "No …"
 * before "… emulated" (a missing behaviour matters more than a stand-in),
 * each kind in matrix order. `hidden` counts the rest, for a "+N more".
 */
export function topCapabilityTags(tags: CapabilityTag[], limit = 3): { shown: CapabilityTag[]; hidden: number } {
  const ranked = [...tags.filter((t) => t.tone === "bad"), ...tags.filter((t) => t.tone !== "bad")];
  return { shown: ranked.slice(0, limit), hidden: Math.max(ranked.length - limit, 0) };
}

/** Behaviours some provider doesn't do natively — the "Differences only" rows. */
export function differingBehaviors(providers: ProviderInfo[], behaviors: BehaviorInfo[]): BehaviorInfo[] {
  return behaviors.filter((b) => providers.some((p) => p.matrix[b.behavior] !== "native"));
}
