import { describe, expect, it } from "vitest";
import { foldToolResults } from "./toolPairing";
import type { TranscriptMessage } from "./types";

describe("foldToolResults", () => {
  it("attaches a matching tool_result onto its tool_use and drops the standalone message", () => {
    const messages: TranscriptMessage[] = [
      { role: "user", text: "hi" },
      { role: "tool", tool: { id: "t1", name: "Bash", input: { command: "ls" } } },
      { role: "tool_result", text: "a.go\nb.go", tool: { id: "t1", name: "" } },
    ];
    const folded = foldToolResults(messages);
    expect(folded).toHaveLength(2);
    expect(folded[1].role).toBe("tool");
    expect(folded[1].tool?.result?.text).toBe("a.go\nb.go");
  });

  it("marks the result as an error when the tool_result was", () => {
    const messages: TranscriptMessage[] = [
      { role: "tool", tool: { id: "t1", name: "Read", path: "img.png" } },
      { role: "tool_result", text: "denied", tool: { id: "t1", name: "" }, is_error: true },
    ];
    const folded = foldToolResults(messages);
    expect(folded[0].tool?.result?.is_error).toBe(true);
  });

  it("drops an orphaned tool_result with no matching tool_use", () => {
    const messages: TranscriptMessage[] = [
      { role: "tool_result", text: "ok", tool: { id: "unknown", name: "" } },
    ];
    expect(foldToolResults(messages)).toHaveLength(0);
  });

  it("drops a tool_result with no ID at all", () => {
    const messages: TranscriptMessage[] = [
      { role: "tool", tool: { id: "t1", name: "Bash" } },
      { role: "tool_result", text: "ok", tool: { name: "" } },
    ];
    const folded = foldToolResults(messages);
    expect(folded).toHaveLength(1);
    expect(folded[0].tool?.result).toBeUndefined();
  });

  it("leaves an unresolved tool_use as the exact same object reference", () => {
    const toolMsg: TranscriptMessage = { role: "tool", tool: { id: "t1", name: "Bash" } };
    const messages: TranscriptMessage[] = [toolMsg];
    const folded = foldToolResults(messages);
    expect(folded[0]).toBe(toolMsg);
  });

  it("passes prose messages through unchanged", () => {
    const userMsg: TranscriptMessage = { role: "user", text: "hi" };
    const folded = foldToolResults([userMsg]);
    expect(folded[0]).toBe(userMsg);
  });
});
