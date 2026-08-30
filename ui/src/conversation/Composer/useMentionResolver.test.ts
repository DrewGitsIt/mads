import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { detectTrigger, useMentionResolver } from "./useMentionResolver";
import { FakeRouterClient } from "../../router/fixtures";
import type { Agent, Model } from "../../router/types";

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

  it("tolerates one interior space for a / trigger, feeding a two-token query", () => {
    expect(detectTrigger("/agent clau", 11)).toEqual({
      type: "/",
      query: "agent clau",
      start: 0,
    });
  });

  it("bails once a / query has two interior spaces", () => {
    expect(detectTrigger("/agent claude opus", 19)).toBeNull();
  });
});

describe("useMentionResolver", () => {
  it("resolves @ mentionables against the faked Router response set", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, undefined, "check @deep", 11, 10),
    );

    expect(result.current.trigger).toEqual({ type: "@", query: "deep", start: 6 });

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    expect(result.current.candidates[0].label).toBe("deep-research");
    expect(result.current.candidates[0].kind).toBe("mentionable");
  });

  it("resolves / commands, badging skills by healthy status", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, "autonomous-executor", undefined, "/veri", 5, 10),
    );

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    const verify = result.current.candidates.find((c) => c.label === "/verify");
    expect(verify?.kind).toBe("command");
    expect(verify?.healthy).toBe(true);
  });

  it("includes the static command list alongside skill capabilities, marking agent/model as groups", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, undefined, "/", 1, 10),
    );

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    const task = result.current.candidates.find((c) => c.label === "/task");
    const agent = result.current.candidates.find((c) => c.label === "/agent");
    const model = result.current.candidates.find((c) => c.label === "/model");
    expect(task?.kind).toBe("command");
    expect(agent?.kind).toBe("command-group");
    expect(model?.kind).toBe("command-group");
  });

  it("has no candidates when there is no active trigger", () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, undefined, "hello world", 11, 10),
    );
    expect(result.current.trigger).toBeNull();
    expect(result.current.candidates).toEqual([]);
  });

  it("descends to registry agents as command-action leaves after '/agent '", async () => {
    const client = new FakeRouterClient();
    const agents: Agent[] = [
      {
        id: "autonomous-executor",
        name: "autonomous-executor",
        prompt: "",
        created_at: "",
        updated_at: "",
      },
      { id: "investigator", name: "investigator", prompt: "", created_at: "", updated_at: "" },
    ];
    vi.spyOn(client, "listAgents").mockResolvedValue(agents);

    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, undefined, "/agent inves", 13, 10),
    );

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    expect(result.current.candidates).toEqual([
      expect.objectContaining({ id: "investigator", label: "investigator", kind: "command-action", group: "agent" }),
    ]);
  });

  it("descends to the session's provider models as command-action leaves after '/model '", async () => {
    const client = new FakeRouterClient();
    const models: Model[] = [
      { id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true },
      { id: "claude-opus-5", name: "Claude Opus 5" },
    ];
    vi.spyOn(client, "listModels").mockResolvedValue(models);

    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, "claudecode", "/model opus", 11, 10),
    );

    await waitFor(() => expect(result.current.candidates.length).toBeGreaterThan(0));
    expect(client.listModels).toHaveBeenCalledWith("claudecode");
    expect(result.current.candidates).toEqual([
      expect.objectContaining({
        id: "claude-opus-5",
        label: "Claude Opus 5",
        kind: "command-action",
        group: "model",
      }),
    ]);
  });

  it("resolves no candidates for '/model ' without a provider — no crash", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, undefined, "/model opus", 11, 10),
    );

    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.candidates).toEqual([]);
  });

  it("resolves no candidates for an unrecognized command head — no crash", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() =>
      useMentionResolver(client, undefined, undefined, "/xyz abc", 8, 10),
    );

    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.candidates).toEqual([]);
  });
});
