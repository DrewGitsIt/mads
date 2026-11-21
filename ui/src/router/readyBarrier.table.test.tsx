// @vitest-environment jsdom
// The ready barrier, table-driven (router/events.ts eventsReady): every
// consumer that takes a REST snapshot on mount must subscribe, wait for the
// bus to know its cursor, and only then read. setupTests primes the bus
// (barrier open), which would hide a read taken too early; each row here
// holds the head read instead (testEventsBarrier.ts) and checks that no
// router call and no non-head fetch goes out before release(), and that the
// read does go out after it. readyBarrier.test.tsx covers the merge (live
// beats snapshot) for the consumers whose snapshot is ordered by seq.
import { act, render, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeRouterClient, fixtureSessions } from "./fixtures";
import type { RouterClient } from "./client";
import { FakeEventSource } from "./testFakeEventSource";
import { holdEventsHead } from "./testEventsBarrier";
import { subscribeSessions } from "./sessionsStore";
import { subscribeSessionRepos } from "./repoStateStore";
import { useAgentIndex } from "./useAgentIndex";
import { FakeMetricsClient } from "../dashboard/client/fixtures";
import type { MetricsClient } from "../dashboard/client/metricsClient";
import { FakePipelineClient } from "../pipeline/client/fixtures";
import type { PipelineClient } from "../pipeline/client/pipelineClient";
import { useTaskStore } from "../tasks/useTaskStore";
import { useSessionWorkspace, useWorkspaceForRef } from "../tasks/useSessionWorkspace";
import { useOpenTaskCounts } from "../features/sessions/state/useOpenTaskCounts";
import { useAllTasks } from "../features/command-palette/state/useAllTasks";
import { useRunTemplates } from "../pipeline/plan/useRunTemplates";
import { usePipelineRun, useResumingRunFold } from "../pipeline/state/usePipelineRun";
import { useRetroHistory } from "../features/retro/state/useRetroHistory";
import { useCapabilities } from "../library/capabilities/useCapabilities";
import { useAgents } from "../library/agents/useAgents";
import { useTemplates } from "../library/templates/useTemplates";
import { useWorkspaces } from "../library/workspaces/useWorkspaces";
import { useLiveActivity } from "../dashboard/state/useLiveActivity";
import { useChangeSet } from "../files/useChangeSet";
import { UsageCard } from "../conversation/Composer/commands/UsageCard";
import { useCommitSettings } from "../conversation/Worktree/useCommitSettings";
import { useWorktreeCards } from "../conversation/Worktree/useWorktreeCards";
import { useDelegateSettings } from "../features/sessions/state/useDelegateSettings";
import { useStreamSpeed } from "../conversation/streaming/streamSpeed";
import { useHeldUpdates } from "../conversation/HeldUpdates/useHeldUpdates";
import { useSessionAuth } from "../conversation/SignIn/useSessionAuth";
import { useProposalStore } from "../context-rail/useProposalStore";
import { useSessionLinks } from "../features/sessions/state/useSessionLinks";
import { useSessionAgentState } from "../features/sessions/state/useSessionAgentState";
import { useOpenDelegateRecords } from "../features/sessions/state/useOpenDelegateRecords";
import { useLinkCommandState } from "../conversation/Composer/commands/useLinkCommandState";
import { ProvisioningStrip } from "../conversation/ProvisioningStrip";
import { DelegateView } from "../features/sessions/delegate/DelegateView";
import { FLAGS } from "../featureFlags";

const SID = "session-active";

/** Records every method call on `target` (by name), then forwards it. */
function recording<T extends object>(target: T, calls: string[]): T {
  return new Proxy(target, {
    get(t, key, receiver) {
      const v = Reflect.get(t, key, receiver);
      if (typeof v !== "function") return v;
      return (...args: unknown[]) => {
        calls.push(String(key));
        return (v as (...a: unknown[]) => unknown).apply(t, args);
      };
    },
  });
}

interface Clients {
  router: RouterClient;
  metrics: MetricsClient;
  pipeline: PipelineClient;
}

// Each row mounts one consumer. Consumers that read through the `router`
// singleton show up as fetches; the rest through the recorded clients. A
// row's third column names client calls allowed before the head: reads no
// live event keeps current, so there is nothing for the cursor to order.
const NOT_LOG_ORDERED = {
  // A session's workspace is fixed at creation; nothing on the bus moves it.
  sessionWorkspace: ["getSession"],
  // The delegate's transcript file, re-read on activity (useDelegateFile).
  delegateFile: ["getDelegateTranscript"],
};
const rows: [name: string, mount: (c: Clients) => void, allowedEarly?: string[]][] = [
  ["sessionsStore", () => void subscribeSessions(() => {})],
  ["repoStateStore", (c) => void subscribeSessionRepos(c.router, SID, () => {})],
  ["useTaskStore", (c) => void renderHook(() => useTaskStore(c.router, SID))],
  ["useOpenTaskCounts", () => void renderHook(() => useOpenTaskCounts())],
  ["useAllTasks", () => void renderHook(() => useAllTasks())],
  ["useSessionWorkspace", (c) => void renderHook(() => useSessionWorkspace(c.router, SID)), NOT_LOG_ORDERED.sessionWorkspace],
  ["useWorkspaceForRef", (c) => void renderHook(() => useWorkspaceForRef(c.router, "ws"))],
  ["useRunTemplates", (c) => void renderHook(() => useRunTemplates(c.router, SID)), NOT_LOG_ORDERED.sessionWorkspace],
  ["usePipelineRun", (c) => void renderHook(() => usePipelineRun(SID, c.pipeline))],
  ["useResumingRunFold", (c) => void renderHook(() => useResumingRunFold(SID, c.pipeline, (r) => r[0]))],
  ["useRetroHistory", () => void renderHook(() => useRetroHistory())],
  ["useCapabilities", () => void renderHook(() => useCapabilities())],
  ["useAgents", () => void renderHook(() => useAgents())],
  ["useTemplates", () => void renderHook(() => useTemplates())],
  ["useWorkspaces", () => void renderHook(() => useWorkspaces())],
  ["useLiveActivity", (c) => void renderHook(() => useLiveActivity(c.metrics))],
  ["useChangeSet", (c) => void renderHook(() => useChangeSet(c.router, SID))],
  ["UsageCard", (c) => void render(<UsageCard routerClient={c.router} sessionId={SID} fallbackWindow={200_000} />)],
  ["useCommitSettings", (c) => void renderHook(() => useCommitSettings(c.router))],
  ["useWorktreeCards", (c) => void renderHook(() => useWorktreeCards(c.router, SID))],
  ["useDelegateSettings", (c) => void renderHook(() => useDelegateSettings(c.router))],
  ["useStreamSpeed", (c) => void renderHook(() => useStreamSpeed(c.router))],
  ["useHeldUpdates", (c) => void renderHook(() => useHeldUpdates(c.router, SID, { waiting: 1 }))],
  ["useSessionAuth", (c) => void renderHook(() => useSessionAuth(c.router, SID))],
  ["useAgentIndex", (c) => void renderHook(() => useAgentIndex(c.router))],
  ["useProposalStore", (c) => void renderHook(() => useProposalStore(c.router, SID))],
  ["useSessionLinks", () => void renderHook(() => useSessionLinks([SID]))],
  ["useSessionAgentState", () => void renderHook(() => useSessionAgentState([]))],
  [
    "useOpenDelegateRecords",
    (c) => void renderHook(() => useOpenDelegateRecords(c.router, { sessionId: SID, harnessId: "m" })),
  ],
  ["useLinkCommandState", (c) => void renderHook(() => useLinkCommandState(c.router, SID, []))],
  ["ProvisioningStrip", (c) => void render(<ProvisioningStrip sessionId={SID} routerClient={c.router} />)],
  [
    "DelegateView",
    (c) =>
      void render(
        <DelegateView sessionId={SID} harnessId="a1" parent={fixtureSessions[SID]} routerClient={c.router} onBack={() => {}} />,
      ),
    NOT_LOG_ORDERED.delegateFile,
  ],
];

describe("the ready barrier, every mount reader", () => {
  let fetches: string[];
  let calls: string[];

  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    FLAGS.linkedSessions = true;
    fetches = [];
    calls = [];
    // Never answers: the row only needs to see the request go out.
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        fetches.push(typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url);
        return new Promise<Response>(() => {});
      }),
    );
  });
  afterEach(() => {
    FLAGS.linkedSessions = false;
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it.each(rows)("%s: no read before the head, a read after it", async (_name, mount, allowedEarly = []) => {
    const clients: Clients = {
      router: recording(new FakeRouterClient(), calls),
      metrics: recording(new FakeMetricsClient(), calls),
      pipeline: recording(new FakePipelineClient([]), calls),
    };
    const head = holdEventsHead();
    mount(clients);
    await act(async () => {});
    await act(async () => {});
    expect(head.headReads()).toBe(1);
    expect(fetches, "fetches before the head").toEqual([]);
    const early = calls.filter((c) => !allowedEarly.includes(c));
    expect(early, "client calls before the head").toEqual([]);
    const before = calls.length;

    await head.release(5);
    await waitFor(() =>
      expect(fetches.length + calls.slice(before).filter((c) => !allowedEarly.includes(c)).length).toBeGreaterThan(0),
    );
  });
});
