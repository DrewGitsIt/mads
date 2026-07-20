import { foldToolResults } from "../../router/toolPairing";
import type { ToolUse, TranscriptMessage } from "../../router/types";

/** HH:MM:SS in the viewer's local timezone — consistent with the rest of the
 * UI (e.g. RunPicker's toLocaleTimeString), not the RFC3339 wire zone. */
export function formatClock(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function elapsedSeconds(sinceIso: string, now = Date.now()): number {
  return Math.max(0, Math.floor((now - new Date(sinceIso).getTime()) / 1000));
}

/**
 * The feed's liveness signal (DESIGN point 4): the transcript's very last
 * entry is a "tool" with no tool_result paired to it (foldToolResults, the
 * same ID-based pairing the chat transcript uses) — still in flight. Only
 * the literal last entry counts: a tool_use with no result that content
 * keeps coming after (e.g. a server-side tool whose result never arrives as
 * a locally-correlatable tool_result, per gap 2) isn't "running", it's just
 * a card with no status to show. Returns the exact turn object from `turns`
 * (foldToolResults preserves reference identity for unresolved tool_uses),
 * so callers can compare a row's turn against this by `===`.
 */
export function pendingTool(turns: TranscriptMessage[]): TranscriptMessage | undefined {
  const folded = foldToolResults(turns);
  const last = folded[folded.length - 1];
  return last?.role === "tool" && !last.tool?.result ? last : undefined;
}

export type FeedRole = "system" | "user" | "agent";

export interface ProseFeedRow {
  kind: "prose";
  role: FeedRole;
  text?: string;
  timestamp?: string;
}

export interface ToolFeedRow {
  kind: "tool";
  tool: ToolUse;
  timestamp?: string;
  /** Still in flight — the trailing tool_use with no paired result yet (see pendingTool). */
  pending: boolean;
}

export type FeedRow = ProseFeedRow | ToolFeedRow;

/**
 * Maps the raw per-block transcript onto one feed row per block: a tool_use
 * folded with its paired tool_result (ChatRenderer/ToolCard's own pairing,
 * reused rather than duplicated), and every entry keeping its own block-level
 * timestamp rather than collapsing onto the enclosing turn's end time.
 *
 * The transcript's very first entry is always the engine's injected stage
 * prompt (engine.go's taskPrompt — this endpoint scopes `turns` to a single
 * stage attempt, so index 0 is never a real user turn). Relabeling it
 * "system" here, by position, means the component never has to sniff the
 * composed prompt's text to tell it apart from a genuine user turn.
 */
export function buildFeedRows(turns: TranscriptMessage[]): FeedRow[] {
  const stageOpener = turns[0];
  const trailingPending = pendingTool(turns);
  return foldToolResults(turns).map((turn) => {
    if (turn.role === "tool" && turn.tool) {
      return { kind: "tool", tool: turn.tool, timestamp: turn.timestamp, pending: turn === trailingPending };
    }
    const role: FeedRole = turn.role !== "user" ? "agent" : turn === stageOpener ? "system" : "user";
    return { kind: "prose", role, text: turn.text, timestamp: turn.timestamp };
  });
}
