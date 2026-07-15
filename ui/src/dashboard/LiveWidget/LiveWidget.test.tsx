import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LiveWidget } from "./LiveWidget";
import { FakeMetricsClient, fixturePipelineRuns, fixtureSessions } from "../client/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("LiveWidget", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders active-session and running-pipeline counts derived from the fixtures", async () => {
    const client = new FakeMetricsClient();
    render(<LiveWidget client={client} />);

    const wantActive = fixtureSessions.filter((s) => s.activity === "active").length;
    const wantRunning = fixturePipelineRuns.filter((r) => r.outcome === "running").length;

    await waitFor(() => expect(screen.getByText(String(wantActive))).toBeTruthy());
    expect(screen.getByText(String(wantRunning))).toBeTruthy();
    expect(screen.getByText("Active sessions")).toBeTruthy();
    expect(screen.getByText("Running pipelines")).toBeTruthy();
  });
});
