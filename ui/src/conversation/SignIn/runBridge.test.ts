import { describe, expect, it } from "vitest";
import { commandLine } from "./runBridge";

describe("commandLine", () => {
  it("puts the env first and quotes only the words that need it", () => {
    expect(commandLine(["/opt/my tools/claude", "auth", "login"], ["CLAUDE_CONFIG_DIR=/Users/me/it's"])).toBe(
      `CLAUDE_CONFIG_DIR='/Users/me/it'\\''s' '/opt/my tools/claude' auth login`,
    );
    expect(commandLine(["/usr/local/bin/claude", "auth", "login"])).toBe("/usr/local/bin/claude auth login");
  });
});
