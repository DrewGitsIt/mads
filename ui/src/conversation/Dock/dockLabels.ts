import { UNKNOWN_AGENT, agentNameForRef } from "../../router/agentRef";
import type { AgentIndex } from "../../router/agentRef";
import type { SessionTrust } from "../../router/types";
import type { LandCardModel, WorktreeCardModel } from "../Worktree/worktreeCards";

/**
 * The persona segment's text: the persona's name, never the stored `agt_…`
 * id. While the persona list is still loading an id reads as a neutral
 * "Agent" rather than flashing "Unknown agent" at something about to resolve.
 */
export function personaLabel(ref: string, agents: AgentIndex | undefined): string {
  return agentNameForRef(ref, agents ?? new Map()) ?? (agents ? UNKNOWN_AGENT : "Agent");
}

/** Below ~700px: a persona name past 14 characters is cut, the full one stays in the tooltip. */
export function shortPersona(label: string): string {
  return label.length > 14 ? `${label.slice(0, 12)}…` : label;
}

/**
 * Below ~700px: the model's short name. Drops a "Claude " family prefix, an
 * ollama-style ":latest" tag and a trailing date stamp — "Claude Sonnet 5" →
 * "Sonnet 5", "qwen3.8:latest" → "qwen3.8", "claude-sonnet-4-5-20250929" →
 * "claude-sonnet-4-5". Never empty: falls back to the label it was given.
 */
export function shortModel(label: string): string {
  const short = label
    .replace(/^Claude\s+/i, "")
    .replace(/:latest$/i, "")
    .replace(/[-@]\d{8}$/, "");
  return short || label;
}

/** Below ~700px: a branch's last path segment, at most 12 characters ("mads/session/ses_20d77a1fc850c53c" → "ses_20d77a1f"). */
export function shortBranch(branch: string): string {
  const last = branch.split("/").filter(Boolean).at(-1) ?? branch;
  return last.length > 12 ? last.slice(0, 12) : last;
}

/** "106.5k", "128k", "1.2M", "640" — the rim label's token counts. */
export function formatTokens(n: number): string {
  const trim = (s: string) => s.replace(/\.0$/, "");
  if (n >= 1_000_000) return `${trim((n / 1_000_000).toFixed(1))}M`;
  if (n >= 1_000) return `${trim((n / 1_000).toFixed(1))}k`;
  return String(Math.round(n));
}

/** The trust dot's colour: the level's risk, read-only (calm) to full access (alarming). */
export function trustTone(trust: SessionTrust | string | undefined): "ok" | "warn" | "info" | "bad" {
  switch (trust || "limited") {
    case "read_only":
      return "ok";
    case "auto_review":
      return "info";
    case "full":
      return "bad";
    default:
      return "warn";
  }
}

function isLandCard(c: WorktreeCardModel): c is LandCardModel {
  return c.kind === "landed" || c.kind === "conflicted" || c.kind === "refused" || c.kind === "autoResolved";
}

/**
 * The newest Land outcome among a session's worktree cards
 * (useWorktreeCards().cards) — what SessionDock's `landOutcome` wants.
 * Undefined when the session has never landed.
 */
export function latestLandOutcome(cards: WorktreeCardModel[]): LandCardModel | undefined {
  for (let i = cards.length - 1; i >= 0; i--) {
    const c = cards[i];
    if (isLandCard(c)) return c;
  }
  return undefined;
}

/**
 * Identity of one Land outcome. A repeated identical refusal keeps its
 * first card's id and counts up instead (collapseRepeats), so the count is
 * part of the key: a Land click that refuses the same way again still reads
 * as a new answer.
 */
export function landOutcomeKey(card: LandCardModel | undefined): string | undefined {
  if (!card) return undefined;
  return `${card.id}:${card.kind === "refused" ? (card.repeats ?? 1) : 1}`;
}
