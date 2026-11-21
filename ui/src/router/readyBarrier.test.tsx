// The ready barrier, consumer by consumer (router/events.ts eventsReady).
// Each consumer must subscribe, wait for the bus to know its cursor, and only
// then take its REST snapshot — and end up with both the snapshot and any
// live event that lands before the snapshot answers. setupTests primes the
// bus (barrier open), which would hide a read taken too early; these hold
// the head read instead (testEventsBarrier.ts).
import { act, render, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeRouterClient, fixtureSessions } from "./fixtures";
import { FakeEventSource } from "./testFakeEventSource";
import { holdEventsHead } from "./testEventsBarrier";
import type { Delegate, Envelope, PipelineRun } from "./types";
import { useWorktreeCards } from "../conversation/Worktree/useWorktreeCards";
import { useOpenDelegateRecords } from "../features/sessions/state/useOpenDelegateRecords";
import { useResumingRunFold } from "../pipeline/state/usePipelineRun";
import { FakePipelineClient } from "../pipeline/client/fixtures";
import { useLinkCommandState } from "../conversation/Composer/commands/useLinkCommandState";
import { DelegateView } from "../features/sessions/delegate/DelegateView";
import { FLAGS } from "../featureFlags";

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

function env(seq: number, type: string, payload: unknown, session_id = "s1"): Envelope {
  return { seq, timestamp: new Date().toISOString(), type, actor: "system", session_id, payload } as Envelope;
}

/** A live frame on whichever open connection carries its type. */
function live(e: Envelope) {
  const source = FakeEventSource.instances.find((s) => !s.closed && s.url.includes(e.type));
  expect(source, `an open connection carrying ${e.type}`).toBeTruthy();
  act(() => source!.emit(e.type, JSON.stringify(e)));
}

function delegate(harness_id: string, state: Delegate["state"], extra: Partial<Delegate> = {}): Delegate {
  return {
    id: `dlg_${harness_id}`,
    session_id: "s1",
    harness_id,
    state,
    runs: 1,
    started_at: "2026-01-01T00:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...extra,
  };
}

describe("the ready barrier", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("useWorktreeCards: history after the head, merged with a live card", async () => {
    const client = new FakeRouterClient();
    const history = deferred<Envelope[]>();
    const read = vi.spyOn(client, "getEvents").mockReturnValue(history.promise);
    const head = holdEventsHead();
    const { result } = renderHook(() => useWorktreeCards(client, "s1"));
    await act(async () => {});
    expect(read).not.toHaveBeenCalled();

    await head.release(5);
    expect(read).toHaveBeenCalledTimes(1);
    live(env(6, "commit.created", { hash: "live", source: "commit", message: "Live" }));
    await act(async () => history.resolve([env(3, "commit.created", { hash: "old", source: "commit", message: "Old" })]));
    await waitFor(() => expect(result.current.cards.map((c) => (c as { hash?: string }).hash)).toEqual(["old", "live"]));
  });

  it("useOpenDelegateRecords: records after the head, under a live change", async () => {
    const client = new FakeRouterClient();
    const records = deferred<Delegate[]>();
    const read = vi.spyOn(client, "getSessionDelegates").mockReturnValue(records.promise);
    const head = holdEventsHead();
    const { result } = renderHook(() => useOpenDelegateRecords(client, { sessionId: "s1", harnessId: "m" }));
    await act(async () => {});
    expect(read).not.toHaveBeenCalled();

    await head.release(5);
    expect(read).toHaveBeenCalledTimes(1);
    // The child finished after the snapshot was read: the live record wins.
    live(env(6, "session.delegate_changed", delegate("kid", "completed", { parent_harness_id: "m" })));
    await act(async () =>
      records.resolve([delegate("m", "running"), delegate("kid", "running", { parent_harness_id: "m" })]),
    );
    await waitFor(() => expect(result.current?.find((d) => d.harness_id === "kid")?.state).toBe("completed"));
    expect(result.current?.map((d) => d.harness_id).sort()).toEqual(["kid", "m"]);
  });

  it("useResumingRunFold: the catch-up after the head, then what arrived live", async () => {
    const client = new FakePipelineClient([]);
    const run: PipelineRun = {
      session_id: "s1",
      run_seq: 10,
      run_id: "r1",
      started_at: "2026-10-19T10:00:00Z",
      outcome: "running",
      tasks_done: 0,
      tasks_blocked: 0,
      tasks_skipped: 0,
      cost_usd: 0,
    };
    const runs = vi.spyOn(client, "listRuns").mockResolvedValue([run]);
    const events = deferred<Envelope[]>();
    vi.spyOn(client, "getRunEvents").mockReturnValue(events.promise);
    const head = holdEventsHead();
    const { result } = renderHook(() => useResumingRunFold("s1", client, (r) => r[0]));
    await act(async () => {});
    expect(runs).not.toHaveBeenCalled();

    await head.release(10);
    expect(runs).toHaveBeenCalledTimes(1);
    live(env(11, "pipeline.completed", { run_id: "r1", outcome: "succeeded" }));
    await act(async () => events.resolve([env(10, "pipeline.started", { run_id: "r1" })]));
    await waitFor(() => expect(result.current.resuming).toBe(false));
    expect(result.current.state.runId).toBe("r1");
    expect(result.current.state.phase).toBe("finished");
  });

  describe("useLinkCommandState", () => {
    beforeEach(() => {
      FLAGS.linkedSessions = true;
    });
    afterEach(() => {
      FLAGS.linkedSessions = false;
    });

    it("the run seed after the head, outranked by a live run event", async () => {
      const client = new FakeRouterClient();
      const runs = deferred<PipelineRun[]>();
      const read = vi.spyOn(client, "metricsPipelineRuns").mockReturnValue(runs.promise);
      const head = holdEventsHead();
      const { result } = renderHook(() => useLinkCommandState(client, "s1", []));
      await act(async () => {});
      expect(read).not.toHaveBeenCalled();

      await head.release(5);
      expect(read).toHaveBeenCalledTimes(1);
      live(env(6, "pipeline.started", { run_id: "r2" }));
      await act(async () => runs.resolve([]));
      expect(result.current.pipelineRunning).toBe(true);
    });
  });

  it("DelegateView: records and history after the head (the no-file stream read too), under a live record", async () => {
    const client = new FakeRouterClient();
    const spawned = { tool_use_id: "toolu_spawn", description: "Audit" };
    const records = deferred<Delegate[]>();
    const read = vi.spyOn(client, "getSessionDelegates").mockReturnValue(records.promise);
    const events = vi.spyOn(client, "getEvents").mockResolvedValue([]);
    vi.spyOn(client, "getDelegateTranscript").mockResolvedValue({ available: false } as never);
    const head = holdEventsHead();
    render(<DelegateView sessionId="s1" harnessId="a1" parent={fixtureSessions["session-active"]} routerClient={client} onBack={() => {}} />);
    await act(async () => {});
    expect(read).not.toHaveBeenCalled();
    expect(events).not.toHaveBeenCalled();

    await head.release(5);
    expect(read).toHaveBeenCalledTimes(1);
    // It finished after the snapshot was read: the live record wins.
    live(env(6, "session.delegate_changed", delegate("a1", "completed", spawned)));
    await act(async () => records.resolve([delegate("a1", "running", spawned)]));
    const header = () => document.querySelector('[data-testid="delegate-view"] header');
    await waitFor(() => expect(header()?.querySelector('[data-status="completed"]')).toBeTruthy());
    // Both the replay read and, once the file is known absent, the stream read.
    await waitFor(() =>
      expect(events.mock.calls.map(([f]) => f.types?.join(","))).toEqual(
        expect.arrayContaining(["session.subagent_task,session.tool_status_changed", "session.subagent_event"]),
      ),
    );
  });
});
