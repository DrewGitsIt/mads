import { describe, expect, it } from "vitest";
import { shellEnv } from "./shellEnv";

describe("shellEnv", () => {
  it("drops npm_* lifecycle vars", () => {
    const result = shellEnv({
      npm_config_prefix: "/repo/ui",
      npm_lifecycle_event: "start",
      NPM_CONFIG_FOO: "bar", // case-insensitive match
      HOME: "/home/drew",
    });
    expect(result).toEqual({ HOME: "/home/drew", TERM_PROGRAM: "mads" });
  });

  it("drops INIT_CWD and NODE, but keeps everything else", () => {
    const result = shellEnv({
      INIT_CWD: "/repo/ui",
      NODE: "/repo/ui/node_modules/.bin/node",
      SHELL: "/bin/zsh",
      DISPLAY: ":0",
      SSH_AUTH_SOCK: "/tmp/ssh.sock",
    });
    expect(result).toEqual({
      SHELL: "/bin/zsh",
      DISPLAY: ":0",
      SSH_AUTH_SOCK: "/tmp/ssh.sock",
      TERM_PROGRAM: "mads",
    });
  });

  it("strips node_modules/.bin and node-gyp-bin entries out of PATH, keeping the rest in order", () => {
    const result = shellEnv({
      PATH: [
        "/repo/ui/node_modules/.bin",
        "/repo/node_modules/.bin",
        "/home/drew/node_modules/.bin",
        "/home/node_modules/.bin",
        "/node_modules/.bin",
        "/usr/local/share/.config/yarn/global/node_modules/.bin/node-gyp-bin",
        "/usr/local/bin",
        "/usr/bin",
      ].join(":"),
    });
    expect(result.PATH).toBe("/usr/local/bin:/usr/bin");
  });

  it("sets TERM_PROGRAM=mads when nothing upstream already set it", () => {
    const result = shellEnv({ HOME: "/home/drew" });
    expect(result.TERM_PROGRAM).toBe("mads");
  });

  it("leaves an existing TERM_PROGRAM alone", () => {
    const result = shellEnv({ TERM_PROGRAM: "vscode" });
    expect(result.TERM_PROGRAM).toBe("vscode");
  });

  it("drops keys with undefined values rather than coercing them to the string 'undefined'", () => {
    const result = shellEnv({ HOME: "/home/drew", UNSET_VAR: undefined });
    expect(result).toEqual({ HOME: "/home/drew", TERM_PROGRAM: "mads" });
  });

  it("is a pure function — same input, same output, no mutation", () => {
    const input = { HOME: "/home/drew", npm_config_foo: "x" };
    const snapshot = { ...input };
    shellEnv(input);
    expect(input).toEqual(snapshot);
  });
});
