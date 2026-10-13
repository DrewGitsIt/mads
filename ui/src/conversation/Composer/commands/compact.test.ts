import { describe, expect, it, vi } from "vitest";
import { FakeRouterClient } from "../../../router/fixtures";
import { compactCommand } from "./compact";
import { argHintFor, availability } from "./registry";
import type { CommandApi, CommandContext } from "./types";

const base: CommandContext = {
  routerClient: new FakeRouterClient(),
  sessionId: "s1",
  provider: "goose",
};
const ctxOf = (compacts: boolean, takesFocus: boolean): CommandContext => ({
  ...base,
  compaction: { compacts, takesFocus },
});

describe("/compact", () => {
  it("is available when the harness compacts, or support is unknown", () => {
    expect(availability(compactCommand, ctxOf(true, true))).toEqual({ ok: true });
    expect(availability(compactCommand, base)).toEqual({ ok: true });
  });

  it("is unavailable with a reason when the harness can't compact", () => {
    expect(availability(compactCommand, ctxOf(false, false))).toEqual({
      ok: false,
      reason: "goose can't compact",
    });
  });

  it("shows [focus] and accepts text when focus is supported", () => {
    const ctx = ctxOf(true, true);
    expect(argHintFor(compactCommand, ctx)).toBe("[focus]");
    expect(compactCommand.argError?.("auth only", ctx)).toBeUndefined();
  });

  it("drops the hint and blocks text when focus isn't supported", () => {
    const ctx = ctxOf(true, false);
    expect(argHintFor(compactCommand, ctx)).toBeUndefined();
    expect(compactCommand.argError?.("auth only", ctx)).toBe(
      "goose can't take compaction instructions — send /compact alone",
    );
    expect(compactCommand.argError?.("", ctx)).toBeUndefined();
  });

  it("passes the focus text (or nothing) through to the compact path", () => {
    const compact = vi.fn();
    const api = { ctx: ctxOf(true, true), compact } as unknown as CommandApi;
    void compactCommand.execute({ args: " keep the schema " }, api);
    void compactCommand.execute({ args: "" }, api);
    expect(compact).toHaveBeenNthCalledWith(1, "keep the schema");
    expect(compact).toHaveBeenNthCalledWith(2, undefined);
  });
});
