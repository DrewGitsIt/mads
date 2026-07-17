import { afterEach, describe, expect, it, vi } from "vitest";
import { FetchPipelineClient } from "./pipelineClient";

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

describe("FetchPipelineClient", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("listTasks GETs /tasks with the session_id query param", async () => {
    const fetchMock = mockFetchOnce([{ id: "t1" }]);
    const client = new FetchPipelineClient();
    const tasks = await client.listTasks("ses_1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:8080/tasks?session_id=ses_1",
    );
    expect(tasks).toEqual([{ id: "t1" }]);
  });

  it("startPipeline POSTs session_id and template_id", async () => {
    const fetchMock = mockFetchOnce({ run_id: "run_1" });
    const client = new FetchPipelineClient();
    const result = await client.startPipeline("ses_1", "tpl_1");
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(url.toString()).toBe("http://localhost:8080/pipelines");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      session_id: "ses_1",
      template_id: "tpl_1",
    });
    expect(result).toEqual({ run_id: "run_1" });
  });

  it("cancelPipeline POSTs /pipelines/:id/cancel", async () => {
    const fetchMock = mockFetchOnce(undefined);
    const client = new FetchPipelineClient();
    await client.cancelPipeline("run_1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:8080/pipelines/run_1/cancel",
    );
  });

  it("retryTask POSTs /tasks/:id/retry and returns the updated task", async () => {
    const fetchMock = mockFetchOnce({ id: "t1", status: "ready", retry_count: 1 });
    const client = new FetchPipelineClient();
    const task = await client.retryTask("t1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:8080/tasks/t1/retry",
    );
    expect(task.retry_count).toBe(1);
  });

  it("pausePipeline, resumePipeline, and reassignTask reject without a network call", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new FetchPipelineClient();

    await expect(client.pausePipeline("run_1")).rejects.toThrow(
      /not supported by the current pipeline engine/,
    );
    await expect(client.resumePipeline("run_1")).rejects.toThrow(
      /not supported by the current pipeline engine/,
    );
    await expect(client.reassignTask("t1", "some-agent")).rejects.toThrow(
      /not supported by the current pipeline engine/,
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("listRuns GETs /metrics/pipelines with the session_id query param", async () => {
    const fetchMock = mockFetchOnce([{ session_id: "ses_1", run_seq: 7, outcome: "completed" }]);
    const client = new FetchPipelineClient();
    const runs = await client.listRuns("ses_1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:8080/metrics/pipelines?session_id=ses_1",
    );
    expect(runs).toEqual([{ session_id: "ses_1", run_seq: 7, outcome: "completed" }]);
  });

  it("getRunEvents GETs /events with session_id, since, and the fold event types", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchPipelineClient();
    await client.getRunEvents("ses_1", 6);
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:8080/events?session_id=ses_1&since=6&types=pipeline.started%2Cpipeline.completed%2Cpipeline.cancelled%2Cpipeline.failed%2Cstage.started%2Cstage.completed%2Cstage.failed%2Cstage.skipped%2Cfile.changed",
    );
  });

  it("streamTaskOutput returns an inert source that never emits", () => {
    const client = new FetchPipelineClient();
    const source = client.streamTaskOutput("run_1", "t1");
    const listener = vi.fn();
    const unsubscribe = source.onChunk(listener);
    unsubscribe();
    source.close();
    expect(listener).not.toHaveBeenCalled();
  });
});
