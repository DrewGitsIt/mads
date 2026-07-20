import type { TranscriptMessage } from "./types";

/**
 * Pairs each "tool_result" message to its "tool" (tool_use) message by ID
 * — the correlation key carried end-to-end from providers/claudecode/
 * transcript.go — and folds the result into that tool_use's `tool.result`.
 * The standalone "tool_result" message is dropped from the output; a
 * tool_result with no matching tool_use (including one with no ID) is
 * dropped too, since there's nothing to attach it to and it has no name or
 * input of its own to render as a card.
 *
 * A "tool" message with no matching result yet is returned as the exact
 * same object reference it came in as (not a copy) — callers that compare
 * turns by identity (the drill-in feed's pending-tool liveness check) keep
 * working unchanged.
 */
export function foldToolResults(messages: TranscriptMessage[]): TranscriptMessage[] {
  const folded: TranscriptMessage[] = [];
  const indexById = new Map<string, number>();

  for (const message of messages) {
    if (message.role === "tool_result") {
      const id = message.tool?.id;
      const idx = id !== undefined ? indexById.get(id) : undefined;
      if (idx === undefined) continue;
      const owner = folded[idx];
      folded[idx] = {
        ...owner,
        tool: {
          ...owner.tool!,
          result: {
            text: message.text,
            is_error: message.is_error,
            timestamp: message.timestamp,
          },
        },
      };
      continue;
    }
    folded.push(message);
    if (message.role === "tool" && message.tool?.id) {
      indexById.set(message.tool.id, folded.length - 1);
    }
  }

  return folded;
}
