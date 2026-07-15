import { describe, expect, it } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { detectTrigger, useMentionResolver } from "./useMentionResolver";
import { FakeRouterClient } from "../../router/fixtures";

describe("detectTrigger", () => {
  it("finds an active @ trigger with the query since the trigger char", () => {
    expect(detectTrigger("hello @cod", 10)).toEqual({
      type: "@",
      query: "cod",
      start: 6,
    });
  });

  it("finds an active / trigger", () => {
    expect(detectTrigger("/tas", 4)).toEqual({ type: "/", query: "tas", start: 0 });
  });

  it("returns null when whitespace intervenes since the trigger char", () => {
    expect(detectTrigger("hello @foo bar", 14)).toBeNull();
  });

  it("returns null when there is no trigger character", () => {
    expect(detectTrigger("hello world", 11)).toBeNull();
  });
});

describe("useMentionResolver", () => {
  it("resolves @ mentionables against the faked Router response set", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, "check @deep", 11, 10),
    );

    expect(result.current.trigger).toEqual({ type: "@", query: "deep", start: 6 });

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    expect(result.current.candidates[0].label).toBe("deep-research");
    expect(result.current.candidates[0].kind).toBe("mentionable");
  });

  it("resolves / commands, badging skills by healthy status", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, "autonomous-executor", "/veri", 5, 10),
    );

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    const verify = result.current.candidates.find((c) => c.label === "/verify");
    expect(verify?.kind).toBe("command");
    expect(verify?.healthy).toBe(true);
  });

  it("includes the static command list alongside skill capabilities", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, "/task", 5, 10),
    );

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    expect(result.current.candidates.some((c) => c.label === "/task")).toBe(true);
  });

  it("has no candidates when there is no active trigger", () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, "hello world", 11, 10),
    );
    expect(result.current.trigger).toBeNull();
    expect(result.current.candidates).toEqual([]);
  });
});
