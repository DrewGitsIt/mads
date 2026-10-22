import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
import type { Envelope, SessionAuth } from "../../router/types";
import { useSessionAuth } from "./useSessionAuth";

// Capture the bus subscriber so a test can deliver envelopes without an EventSource.
const subs: { types: string[]; onEvent: (e: Envelope) => void }[] = [];
vi.mock("../../router/events", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  subscribeToEvents: (params: { types: string[] }, onEvent: (e: Envelope) => void) => {
    const sub = { ...params, onEvent };
    subs.push(sub);
    return () => subs.splice(subs.indexOf(sub), 1);
  },
}));

beforeEach(() => {
  subs.length = 0;
});

const signedOut: SessionAuth = { provider: "claude-code", account: "claude-code:/c", signed_out: true };
const changed = (): Envelope => ({
  seq: 1,
  timestamp: "",
  type: "account.auth_changed",
  actor: "data",
  payload: { account: "claude-code:/c", provider: "claude-code", signed_out: false },
});

describe("useSessionAuth", () => {
  it("fetches the session's auth and refetches when any login's state flips", async () => {
    const client = new FakeRouterClient();
    client.sessionAuth.set("ses_a", signedOut);
    const { result } = renderHook(() => useSessionAuth(client, "ses_a"));
    await waitFor(() => expect(result.current.auth?.signed_out).toBe(true));
    expect(subs.map((s) => s.types)).toEqual([["account.auth_changed"]]);

    client.sessionAuth.set("ses_a", { ...signedOut, signed_out: false });
    act(() => subs.forEach((s) => s.onEvent(changed())));
    await waitFor(() => expect(result.current.auth?.signed_out).toBe(false));
  });

  it("stays undefined when the Router has no auth for the session", async () => {
    const client = new FakeRouterClient();
    const spy = vi.spyOn(client, "getSessionAuth");
    const { result } = renderHook(() => useSessionAuth(client, "ses_a"));
    await waitFor(() => expect(spy).toHaveBeenCalled());
    await act(async () => {});
    expect(result.current.auth).toBeUndefined();
  });
});
