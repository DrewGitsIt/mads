import { describe, expect, it } from "vitest";
import {
  formatTokens,
  landOutcomeKey,
  latestLandOutcome,
  shortBranch,
  shortModel,
  shortPersona,
  trustTone,
} from "./dockLabels";
import type { WorktreeCardModel } from "../Worktree/worktreeCards";

describe("dock labels", () => {
  it("formats token counts the way the rim prints them", () => {
    expect(formatTokens(106_496)).toBe("106.5k");
    expect(formatTokens(128_000)).toBe("128k");
    expect(formatTokens(1_200_000)).toBe("1.2M");
    expect(formatTokens(640)).toBe("640");
  });

  it("shortens model names for a narrow dock", () => {
    expect(shortModel("Claude Sonnet 5")).toBe("Sonnet 5");
    expect(shortModel("qwen3.8:latest")).toBe("qwen3.8");
    expect(shortModel("claude-sonnet-4-5-20250929")).toBe("claude-sonnet-4-5");
    expect(shortModel("gpt-oss:20b")).toBe("gpt-oss:20b");
  });

  it("shortens a branch to its last segment's first 12 characters", () => {
    expect(shortBranch("mads/session/ses_20d77a1fc850c53c")).toBe("ses_20d77a1f");
    expect(shortBranch("main")).toBe("main");
  });

  it("cuts only long persona names", () => {
    expect(shortPersona("Goose")).toBe("Goose");
    expect(shortPersona("mads-coding-partner")).toBe("mads-coding-…");
  });

  it("colours trust by risk, unset reading as limited", () => {
    expect(trustTone(undefined)).toBe("warn");
    expect(trustTone("read_only")).toBe("ok");
    expect(trustTone("auto_review")).toBe("info");
    expect(trustTone("full")).toBe("bad");
  });

  it("finds the newest Land outcome among the cards", () => {
    const cards: WorktreeCardModel[] = [
      { kind: "refused", id: "wt-1", seq: 1, reason: "busy", latest: false },
      { kind: "committed", id: "wt-2", seq: 2, hash: "h", subject: "s", drafted: false, undone: false, tip: true },
      { kind: "landed", id: "wt-3", seq: 3, fastForwarded: false },
      { kind: "trustChanged", id: "wt-4", seq: 4, from: "limited", to: "full", restarted: true },
    ];
    expect(latestLandOutcome(cards)?.id).toBe("wt-3");
    expect(latestLandOutcome(cards.slice(1, 2))).toBeUndefined();
  });

  it("keys a repeated refusal apart from its first showing", () => {
    const first = { kind: "refused" as const, id: "wt-1", seq: 1, reason: "busy", latest: true };
    expect(landOutcomeKey(first)).not.toBe(landOutcomeKey({ ...first, repeats: 2 }));
    expect(landOutcomeKey(undefined)).toBeUndefined();
  });
});
