import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FetchPipelineClient } from "./pipelineClient";
import { FakeEventSource } from "../../router/testFakeEventSource";

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
      "http://localhost:17373/tasks?session_id=ses_1",
    );
    expect(tasks).toEqual([{ id: "t1" }]);
  });

  it("startPipeline POSTs session_id and template_id", async () => {
    const fetchMock = mockFetchOnce({ run_id: "run_1" });
    const client = new FetchPipelineClient();
    const result = await client.startPipeline("ses_1", "tpl_1");
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(url.toString()).toBe("http://localhost:17373/pipelines");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      session_id: "ses_1",
      template_id: "tpl_1",
    });
    expect(result).toEqual({ run_id: "run_1" });
  });

  it("startPipeline sends exclude_task_ids when some are given", async () => {
    const fetchMock = mockFetchOnce({ run_id: "run_1" });
    const client = new FetchPipelineClient();
    await client.startPipeline("ses_1", "tpl_1", { excludeTaskIds: ["t2", "t3"] });
    const [, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({
      session_id: "ses_1",
      template_id: "tpl_1",
      exclude_task_ids: ["t2", "t3"],
    });
  });

  it("startPipeline omits exclude_task_ids when the list is empty", async () => {
    const fetchMock = mockFetchOnce({ run_id: "run_1" });
    const client = new FetchPipelineClient();
    await client.startPipeline("ses_1", "tpl_1", { excludeTaskIds: [] });
    const [, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(JSON.parse(init.body as string)).not.toHaveProperty("exclude_task_ids");
  });

  it("cancelPipeline POSTs /pipelines/:id/cancel", async () => {
    const fetchMock = mockFetchOnce(undefined);
    const client = new FetchPipelineClient();
    await client.cancelPipeline("run_1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:17373/pipelines/run_1/cancel",
    );
  });

  it("retryTask POSTs /tasks/:id/retry and returns the updated task", async () => {
    const fetchMock = mockFetchOnce({ id: "t1", status: "ready", retry_count: 1 });
    const client = new FetchPipelineClient();
    const task = await client.retryTask("t1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:17373/tasks/t1/retry",
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
      "http://localhost:17373/metrics/pipelines?session_id=ses_1",
    );
    expect(runs).toEqual([{ session_id: "ses_1", run_seq: 7, outcome: "completed" }]);
  });

  it("getRunEvents pages past the 500-event batch limit, resuming from each page's last seq", async () => {
    const page = (from: number, n: number) =>
      Array.from({ length: n }, (_, i) => ({
        seq: from + i,
        timestamp: "2026-09-30T04:21:03Z",
        type: "stage.started",
        session_id: "ses_1",
        actor: "pipeline",
        payload: {},
      }));
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(page(7, 500)), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(page(507, 3)), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const events = await new FetchPipelineClient().getRunEvents("ses_1", 6);
    expect(events).toHaveLength(503);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][0].toString()).toContain("since=506");
  });

  it("getRunEvents GETs /events with session_id, since, and the fold event types", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchPipelineClient();
    await client.getRunEvents("ses_1", 6);
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:17373/events?session_id=ses_1&since=6&types=pipeline.started%2Cpipeline.completed%2Cpipeline.cancelled%2Cpipeline.failed%2Cstage.started%2Cstage.completed%2Cstage.failed%2Cstage.skipped%2Cstage.retried%2Cstage.picked_up%2Cfile.changed%2Ctask.status_changed",
    );
  });

  it("getPickup GETs the session's pickup answer for a template", async () => {
    const answer = {
      shared_directory: false,
      tasks: [{ task_id: "t1", mode: "resume", from_stage: "code_review", last_run_seq: 4, template_name: "tpl", template_version: 2 }],
    };
    const fetchMock = mockFetchOnce(answer);
    const got = await new FetchPipelineClient().getPickup("ses_1", "tpl_1");
    expect(fetchMock.mock.calls[0][0].toString()).toBe(
      "http://localhost:17373/sessions/ses_1/pipeline/pickup?template=tpl_1",
    );
    expect(got).toEqual(answer);
  });

  describe("streamTaskOutput", () => {
    beforeEach(() => {
      FakeEventSource.instances = [];
      vi.stubGlobal("EventSource", FakeEventSource);
    });

    it("opens an EventSource against the task output route", () => {
      const client = new FetchPipelineClient();
      client.streamTaskOutput("ses_1", "t1");
      expect(FakeEventSource.instances[0].url).toBe(
        "http://localhost:17373/sessions/ses_1/pipeline/tasks/t1/output",
      );
    });

    it("adds run, stage and attempt query params when given", () => {
      const client = new FetchPipelineClient();
      client.streamTaskOutput("ses_1", "t1", { run: 42, stage: "code_review", attempt: 2 });
      expect(FakeEventSource.instances[0].url).toBe(
        "http://localhost:17373/sessions/ses_1/pipeline/tasks/t1/output?run=42&stage=code_review&attempt=2",
      );
    });

    it("sends only the params that are set", () => {
      const client = new FetchPipelineClient();
      client.streamTaskOutput("ses_1", "t1", { run: 7 });
      expect(FakeEventSource.instances[0].url).toBe(
        "http://localhost:17373/sessions/ses_1/pipeline/tasks/t1/output?run=7",
      );
    });

    it("delivers JSON-decoded message frames to onChunk listeners", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskOutput("ses_1", "t1");
      const listener = vi.fn();
      source.onChunk(listener);

      FakeEventSource.instances[0].emit("message", JSON.stringify("hello"));

      expect(listener).toHaveBeenCalledWith("hello");
    });

    it("unsubscribing onChunk stops delivering further frames", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskOutput("ses_1", "t1");
      const listener = vi.fn();
      const unsubscribe = source.onChunk(listener);
      unsubscribe();

      FakeEventSource.instances[0].emit("message", JSON.stringify("hello"));

      expect(listener).not.toHaveBeenCalled();
    });

    it("closes the EventSource on the server's terminal 'end' event", () => {
      const client = new FetchPipelineClient();
      client.streamTaskOutput("ses_1", "t1");
      const es = FakeEventSource.instances[0];

      es.emit("end", "");

      expect(es.closed).toBe(true);
    });

    it("tells onEnd listeners about the 'end', and a late one at once", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskOutput("ses_1", "t1");
      const early = vi.fn();
      source.onEnd?.(early);

      FakeEventSource.instances[0].emit("end", "");
      expect(early).toHaveBeenCalledTimes(1);

      const late = vi.fn();
      source.onEnd?.(late);
      expect(late).toHaveBeenCalledTimes(1);
    });

    it("close() closes the underlying EventSource", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskOutput("ses_1", "t1");
      source.close();

      expect(FakeEventSource.instances[0].closed).toBe(true);
    });
  });

  describe("streamTaskTurns", () => {
    beforeEach(() => {
      FakeEventSource.instances = [];
      vi.stubGlobal("EventSource", FakeEventSource);
    });

    it("opens an EventSource against the task turns route", () => {
      const client = new FetchPipelineClient();
      client.streamTaskTurns("ses_1", "t1");
      expect(FakeEventSource.instances[0].url).toBe(
        "http://localhost:17373/sessions/ses_1/pipeline/tasks/t1/turns",
      );
    });

    it("adds run, stage and attempt query params when given", () => {
      const client = new FetchPipelineClient();
      client.streamTaskTurns("ses_1", "t1", { run: 42, stage: "executor", attempt: 1 });
      expect(FakeEventSource.instances[0].url).toBe(
        "http://localhost:17373/sessions/ses_1/pipeline/tasks/t1/turns?run=42&stage=executor&attempt=1",
      );
    });

    it("delivers JSON-decoded turn arrays to onTurns listeners", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskTurns("ses_1", "t1");
      const listener = vi.fn();
      source.onTurns(listener);

      FakeEventSource.instances[0].emit(
        "message",
        JSON.stringify([{ role: "user", text: "hi" }]),
      );

      expect(listener).toHaveBeenCalledWith([{ role: "user", text: "hi" }]);
    });

    it("unsubscribing onTurns stops delivering further frames", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskTurns("ses_1", "t1");
      const listener = vi.fn();
      const unsubscribe = source.onTurns(listener);
      unsubscribe();

      FakeEventSource.instances[0].emit("message", JSON.stringify([]));

      expect(listener).not.toHaveBeenCalled();
    });

    it("closes the EventSource on the server's terminal 'end' event", () => {
      const client = new FetchPipelineClient();
      client.streamTaskTurns("ses_1", "t1");
      const es = FakeEventSource.instances[0];

      es.emit("end", "");

      expect(es.closed).toBe(true);
    });

    it("close() closes the underlying EventSource", () => {
      const client = new FetchPipelineClient();
      const source = client.streamTaskTurns("ses_1", "t1");
      source.close();

      expect(FakeEventSource.instances[0].closed).toBe(true);
    });
  });
});
