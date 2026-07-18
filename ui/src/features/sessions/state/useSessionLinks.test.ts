import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionLinks } from "./useSessionLinks";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { FLAGS } from "../../../featureFlags";
import type { Link } from "../../../router/types";

function makeLink(overrides: Partial<Link>): Link {
  return {
    id: "link-1",
    granting_session_id: "s1",
    subject_session_id: "s2",
    grants: ["read"],
    cadence: "per_turn",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("useSessionLinks", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    FLAGS.linkedSessions = true;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    FLAGS.linkedSessions = false;
  });

  it("fetches per session and dedupes links shared by both endpoints", async () => {
    const link = makeLink({ id: "l1", granting_session_id: "s1", subject_session_id: "s2" });
    const fetchMock = vi
      .fn()
      .mockImplementation(() => Promise.resolve(new Response(JSON.stringify([link]), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1", "s2"]));

    await waitFor(() => expect(result.current).toEqual([link]));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("returns nothing and fetches nothing when the flag is off", async () => {
    FLAGS.linkedSessions = false;
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1"]));

    await waitFor(() => expect(result.current).toEqual([]));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refetches on a link.created event", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("[]", { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeLink({ id: "l1" })]), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionLinks(["s1"]));
    await waitFor(() => expect(result.current).toEqual([]));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "link.created",
        JSON.stringify({ seq: 1, type: "link.created", actor: "user", link_id: "l1" }),
      );
    });

    await waitFor(() => expect(result.current).toEqual([makeLink({ id: "l1" })]));
  });
});
