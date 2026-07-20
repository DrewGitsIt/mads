import { foldToolResults } from "../../router/toolPairing";
import type { ToolUse, TranscriptMessage } from "../../router/types";

/** Middle-truncates value, keeping both ends visible — full value stays
 * available via the caller's title attribute / expand-on-click. */
export function truncateMiddle(value: string, max = 72): string {
  if (value.length <= max) return value;
  const keep = max - 1; // room for the ellipsis
  const head = Math.ceil(keep / 2);
  const tail = Math.floor(keep / 2);
  return `${value.slice(0, head)}…${value.slice(value.length - tail)}`;
}

/** HH:MM:SS out of an RFC3339 timestamp, string slicing rather than Date
 * parsing so rendering doesn't depend on the viewer's local timezone. */
export function formatClock(iso: string): string {
  const match = iso.match(/T(\d{2}:\d{2}:\d{2})/);
  return match ? match[1] : iso;
}

/** The salient argument for a compact tool row — the command for Bash, the
 * touched file path otherwise, blank when the wire shape has neither. */
export function toolArgument(tool: ToolUse): string {
  if (tool.path) return tool.path;
  const input = tool.input as Record<string, unknown> | undefined;
  if (typeof input?.command === "string") return input.command;
  if (typeof input?.file_path === "string") return input.file_path;
  return "";
}

export function elapsedSeconds(sinceIso: string, now = Date.now()): number {
  return Math.max(0, Math.floor((now - new Date(sinceIso).getTime()) / 1000));
}

/**
 * The feed's liveness signal (DESIGN point 4): the trailing "tool" with no
 * tool_result paired to it (foldToolResults, the same ID-based pairing the
 * chat transcript uses) is still in flight. A pipeline stage runs one tool
 * call at a time, so there's never more than one pending turn to find.
 * Returns the exact turn object from `turns` (foldToolResults preserves
 * reference identity for unresolved tool_uses), so callers can compare a
 * row's turn against this by `===`.
 */
export function pendingTool(turns: TranscriptMessage[]): TranscriptMessage | undefined {
  const folded = foldToolResults(turns);
  for (let i = folded.length - 1; i >= 0; i--) {
    const turn = folded[i];
    if (turn.role === "tool") {
      return turn.tool?.result ? undefined : turn;
    }
  }
  return undefined;
}
