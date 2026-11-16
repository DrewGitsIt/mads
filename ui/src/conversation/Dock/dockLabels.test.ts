import { describe, expect, it } from "vitest";
import {
  formatTokens,
  shortBranch,
  shortModel,
  shortPersona,
  trustTone,
} from "./dockLabels";

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
});
