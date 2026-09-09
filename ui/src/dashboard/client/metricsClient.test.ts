import { afterEach, describe, expect, it, vi } from "vitest";
import { FetchMetricsClient } from "./metricsClient";

function mockFetchOnce(body: unknown, ok = true, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok,
    status,
    statusText: ok ? "OK" : "Error",
    json: async () => body,
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("FetchMetricsClient", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("metricsCost GETs /metrics/cost with no session_id param", async () => {
    const fetchMock = mockFetchOnce({ by_day: [], by_session: [], by_pipeline_run: [] });
    const client = new FetchMetricsClient();
    const result = await client.metricsCost();
    expect(fetchMock.mock.calls[0][0].toString()).toBe("http://localhost:17373/metrics/cost");
    expect(result).toEqual({ by_day: [], by_session: [], by_pipeline_run: [] });
  });

  it("metricsAgentLeaderboard GETs /metrics/agents", async () => {
    const fetchMock = mockFetchOnce([{ agent: "executor" }]);
    const client = new FetchMetricsClient();
    const result = await client.metricsAgentLeaderboard();
    expect(fetchMock.mock.calls[0][0].toString()).toBe("http://localhost:17373/metrics/agents");
    expect(result).toEqual([{ agent: "executor" }]);
  });

  it("metricsTaskOutcomes GETs /metrics/tasks", async () => {
    const fetchMock = mockFetchOnce({ counts: {}, total: 0, completion_rate: 0 });
    const client = new FetchMetricsClient();
    await client.metricsTaskOutcomes();
    expect(fetchMock.mock.calls[0][0].toString()).toBe("http://localhost:17373/metrics/tasks");
  });

  it("metricsCapabilityUsage GETs /metrics/capabilities", async () => {
    const fetchMock = mockFetchOnce([{ capability: "git-review", type: "skill", invocations: 0 }]);
    const client = new FetchMetricsClient();
    const result = await client.metricsCapabilityUsage();
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:17373/metrics/capabilities",
    );
    expect(result).toEqual([{ capability: "git-review", type: "skill", invocations: 0 }]);
  });

  it("metricsPipelineRuns GETs /metrics/pipelines", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchMetricsClient();
    await client.metricsPipelineRuns();
    expect(fetchMock.mock.calls[0][0].toString()).toBe("http://localhost:17373/metrics/pipelines");
  });

  it("listSessions GETs /sessions", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchMetricsClient();
    await client.listSessions();
    expect(fetchMock.mock.calls[0][0].toString()).toBe("http://localhost:17373/sessions");
  });
});
