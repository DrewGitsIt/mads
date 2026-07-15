import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useLiveActivity } from "./useLiveActivity";
import { FakeMetricsClient, fixturePipelineRuns, fixtureSessions } from "../client/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { PipelineRun, Session } from "../../router/types";

describe("useLiveActivity", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("derives active-session and running-pipeline counts on mount", async () => {
    const client = new FakeMetricsClient();
    const { result } = renderHook(() => useLiveActivity(client));

    await waitFor(() => expect(result.current.loading).toBe(false));

    const wantActive = fixtureSessions.filter((s) => s.activity === "active").length;
    const wantRunning = fixturePipelineRuns.filter((r) => r.outcome === "running").length;
    expect(result.current.activeSessionCount).toBe(wantActive);
    expect(result.current.runningPipelineCount).toBe(wantRunning);
  });

  it("refetches counts after a session.* or pipeline.* SSE event", async () => {
    const client = new FakeMetricsClient();
    const runningRun: PipelineRun = { ...fixturePipelineRuns[0], outcome: "running" };
    const extraSession: Session = { ...fixtureSessions[0], id: "ses_extra", activity: "active" };

    let call = 0;
    client.listSessions = async () => (call === 0 ? fixtureSessions : [...fixtureSessions, extraSession]);
    client.metricsPipelineRuns = async () => {
      call += 1;
      return call === 1 ? fixturePipelineRuns : [runningRun];
    };

    const { result } = renderHook(() => useLiveActivity(client));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.runningPipelineCount).toBe(0);

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "pipeline.started",
        JSON.stringify({ seq: 1, type: "pipeline.started", actor: "user", session_id: "ses_extra" }),
      );
    });

    await waitFor(() => expect(result.current.runningPipelineCount).toBe(1));
    const wantActive = fixtureSessions.filter((s) => s.activity === "active").length + 1;
    expect(result.current.activeSessionCount).toBe(wantActive);
  });
});
