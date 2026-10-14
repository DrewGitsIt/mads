import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { detectTrigger, sameTrigger, useMentionResolver } from "./useMentionResolver";
import { FakeRouterClient } from "../../router/fixtures";
import type { Model } from "../../router/types";
import { COMMANDS } from "./commands/registry";
import type { CommandContext, CommandDescriptor } from "./commands/types";

function ctxFor(
  client: FakeRouterClient,
  extra: Partial<CommandContext> = {},
): CommandContext {
  return { routerClient: client, sessionId: "s1", ...extra };
}

describe("detectTrigger", () => {
  it("finds an active @ trigger with the query since the trigger char", () => {
    expect(detectTrigger("hello @cod", 10)).toEqual({
      type: "@",
      query: "cod",
      start: 6,
    });
  });

  it("finds an active / trigger", () => {
    expect(detectTrigger("/tas", 4)).toEqual({
      type: "/",
      query: "tas",
      start: 0,
    });
  });

  it("finds an active # trigger", () => {
    expect(detectTrigger("run #veri", 9)).toEqual({
      type: "#",
      query: "veri",
      start: 4,
    });
  });

  it("returns null when whitespace intervenes since an @ or # trigger char", () => {
    expect(detectTrigger("hello @foo bar", 14)).toBeNull();
    expect(detectTrigger("# Heading", 9)).toBeNull();
  });

  it("returns null when there is no trigger character", () => {
    expect(detectTrigger("hello world", 11)).toBeNull();
  });

  it("tolerates one interior space for a / trigger, feeding a two-token query", () => {
    expect(detectTrigger("/model clau", 11)).toEqual({
      type: "/",
      query: "model clau",
      start: 0,
    });
  });

  it("bails once a / query has two interior spaces", () => {
    expect(detectTrigger("/model claude opus", 19)).toBeNull();
  });
});

describe("useMentionResolver", () => {
  it("resolves @ mentionables against the faked Router response set", async () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client);
    const { result } = renderHook(() =>
      useMentionResolver(ctx, "check @deep", 11, 10),
    );

    expect(result.current.trigger).toEqual({
      type: "@",
      query: "deep",
      start: 6,
    });

    await waitFor(() =>
      expect(result.current.candidates.length).toBeGreaterThan(0),
    );
    expect(result.current.candidates[0].label).toBe("deep-research");
    expect(result.current.candidates[0].kind).toBe("mentionable");
  });

  it("resolves # skills, badging them by healthy status", async () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client, { agentId: "autonomous-executor" });
    const { result } = renderHook(() =>
      useMentionResolver(ctx, "#veri", 5, 10),
    );

    await waitFor(() =>
      expect(result.current.candidates.length).toBeGreaterThan(0),
    );
    const verify = result.current.candidates.find((c) => c.label === "#verify");
    expect(verify?.kind).toBe("skill");
    expect(verify?.insertText).toBe("#verify");
    expect(verify?.healthy).toBe(true);
    expect(result.current.candidates.every((c) => c.kind === "skill")).toBe(
      true,
    );
  });

  it("lists only registry commands for /, never skills", async () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client, { provider: "claudecode" });
    const { result } = renderHook(() => useMentionResolver(ctx, "/", 1, 10));

    await waitFor(() =>
      expect(result.current.candidates.length).toBeGreaterThan(0),
    );
    // Exactly the visible registry, in order (with the linked-sessions flag
    // off, /new's link variants aren't in it).
    expect(result.current.candidates.map((c) => c.label)).toEqual(
      COMMANDS.filter((c) => c.visible?.(ctx) ?? true).map((c) => `/${c.name}`),
    );
    expect(result.current.candidates.map((c) => c.label)).not.toContain("/new-leader");
    expect(result.current.candidates.every((c) => c.kind === "command")).toBe(
      true,
    );
    expect(result.current.candidates[0]).toEqual(
      expect.objectContaining({ command: "task", argHint: "<title>" }),
    );
  });

  it("matches commands through their aliases, exact matches first", async () => {
    const commands: CommandDescriptor[] = [
      { name: "usage-report", description: "", execute: vi.fn() },
      {
        name: "usage",
        aliases: ["status", "context"],
        description: "",
        execute: vi.fn(),
      },
    ];
    const client = new FakeRouterClient();
    const ctx = ctxFor(client);

    const { result, rerender } = renderHook(
      ({ value }) => useMentionResolver(ctx, value, value.length, 10, commands),
      { initialProps: { value: "/stat" } },
    );
    await waitFor(() => expect(result.current.candidates.length).toBe(1));
    expect(result.current.candidates[0]).toEqual(
      expect.objectContaining({
        label: "/usage",
        alias: "status",
        insertText: "/usage",
      }),
    );

    rerender({ value: "/usage" });
    await waitFor(() => expect(result.current.candidates.length).toBe(2));
    expect(result.current.candidates.map((c) => c.label)).toEqual([
      "/usage",
      "/usage-report",
    ]);
  });

  it("marks commands whose available() says no as disabled with the reason", async () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client); // no provider → /model is unavailable
    const { result } = renderHook(() => useMentionResolver(ctx, "/mod", 4, 10));

    await waitFor(() => expect(result.current.candidates.length).toBe(1));
    expect(result.current.candidates[0]).toEqual(
      expect.objectContaining({
        label: "/model",
        disabledReason: "This session has no provider yet",
      }),
    );
  });

  it("leaves out commands whose visible() is false", async () => {
    const commands: CommandDescriptor[] = [
      { name: "new", description: "", execute: vi.fn() },
      {
        name: "new-leader",
        description: "",
        visible: () => false,
        execute: vi.fn(),
      },
    ];
    const client = new FakeRouterClient();
    const ctx = ctxFor(client);
    const { result } = renderHook(() =>
      useMentionResolver(ctx, "/new", 4, 10, commands),
    );

    await waitFor(() => expect(result.current.candidates.length).toBe(1));
    expect(result.current.candidates[0].label).toBe("/new");
  });

  it("has no candidates when there is no active trigger", () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client);
    const { result } = renderHook(() =>
      useMentionResolver(ctx, "hello world", 11, 10),
    );
    expect(result.current.trigger).toBeNull();
    expect(result.current.candidates).toEqual([]);
  });

  it("descends to the session's provider models as command-option leaves after '/model '", async () => {
    const client = new FakeRouterClient();
    const models: Model[] = [
      { id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true },
      { id: "claude-opus-5", name: "Claude Opus 5" },
    ];
    vi.spyOn(client, "listModels").mockResolvedValue(models);
    const ctx = ctxFor(client, { provider: "claudecode" });

    const { result } = renderHook(() =>
      useMentionResolver(ctx, "/model opus", 11, 10),
    );

    await waitFor(() =>
      expect(result.current.candidates.length).toBeGreaterThan(0),
    );
    expect(client.listModels).toHaveBeenCalledWith("claudecode");
    expect(result.current.candidates).toEqual([
      expect.objectContaining({
        id: "claude-opus-5",
        label: "Claude Opus 5",
        kind: "command-option",
        group: "model",
      }),
    ]);
  });

  it("resolves no candidates for '/model ' without a provider — no crash", async () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client);
    const { result } = renderHook(() =>
      useMentionResolver(ctx, "/model opus", 11, 10),
    );

    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.candidates).toEqual([]);
  });

  it("resolves no candidates for an unrecognized command head — no crash", async () => {
    const client = new FakeRouterClient();
    const ctx = ctxFor(client);
    const { result } = renderHook(() =>
      useMentionResolver(ctx, "/xyz abc", 8, 10),
    );

    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.candidates).toEqual([]);
  });
});

describe("candidatesFor", () => {
  it("names the trigger the rows were resolved for, lagging the text by the debounce", async () => {
    const ctx = ctxFor(new FakeRouterClient());
    const { result, rerender } = renderHook(
      ({ value }) => useMentionResolver(ctx, value, value.length, 30),
      { initialProps: { value: "/c" } },
    );
    await waitFor(() =>
      expect(sameTrigger(result.current.candidatesFor, result.current.trigger)).toBe(true),
    );
    expect(result.current.candidatesFor).toEqual({ type: "/", query: "c", start: 0 });

    rerender({ value: "/context" });
    expect(result.current.trigger?.query).toBe("context");
    expect(sameTrigger(result.current.candidatesFor, result.current.trigger)).toBe(false);
    await waitFor(() =>
      expect(result.current.candidatesFor?.query).toBe("context"),
    );
    expect(result.current.candidates[0].command).toBe("usage");
  });
});
