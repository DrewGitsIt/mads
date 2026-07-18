import { afterEach, describe, expect, it, vi } from "vitest";
import { FetchRouterClient, RouterValidationError } from "./client";
import type { Agent } from "./types";

function mockFetchOnce(body: unknown, ok = true, status = 200) {
  const response = {
    ok,
    status,
    statusText: ok ? "OK" : "Error",
    json: async () => body,
    text: async () => JSON.stringify(body),
    clone() {
      return response;
    },
  };
  const fetchMock = vi.fn().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("FetchRouterClient", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("getSession fetches /sessions/:id and returns the parsed body", async () => {
    const fetchMock = mockFetchOnce({ id: "s1", name: "x", activity: "active" });
    const client = new FetchRouterClient("http://router.local");
    const session = await client.getSession("s1");
    expect(fetchMock).toHaveBeenCalledWith("http://router.local/sessions/s1");
    expect(session.id).toBe("s1");
  });

  it("getTranscript includes the page query param when given", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getTranscript("s1", "2");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/transcript?page=2",
    );
  });

  it("sendMessage POSTs JSON and throws on a non-ok response", async () => {
    mockFetchOnce(undefined, false, 500);
    const client = new FetchRouterClient("http://router.local");
    await expect(
      client.sendMessage("s1", { text: "hi" }),
    ).rejects.toThrow(/500/);
  });

  it("getMentionables encodes the query string", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getMentionables("some file");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/mentionables?q=some%20file",
    );
  });

  it("getSkillCapabilities omits the agent query param when absent", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getSkillCapabilities();
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/capabilities",
    );
  });

  it("getFileChanges fetches /sessions/:id/files", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getFileChanges("s1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/files",
    );
  });

  it("getFileChanges includes the task_id query param when given", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getFileChanges("s1", "tsk_1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/files?task_id=tsk_1",
    );
  });

  it("getCommits fetches /sessions/:id/commits", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getCommits("s1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/commits",
    );
  });

  it("getFileDiff encodes the path query param", async () => {
    const fetchMock = mockFetchOnce({
      path: "a.go",
      original: "old",
      modified: "new",
    });
    const client = new FetchRouterClient("http://router.local");
    await client.getFileDiff("s1", "internal/a.go");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/changes?path=internal%2Fa.go",
    );
  });

  it("openInEditor POSTs the path and throws on a non-ok response", async () => {
    mockFetchOnce(undefined, false, 500);
    const client = new FetchRouterClient("http://router.local");
    await expect(client.openInEditor("a.go")).rejects.toThrow(/500/);
  });

  it("mergeSession POSTs to /sessions/:id/merge with the target branch", async () => {
    const fetchMock = mockFetchOnce({ conflicted: false });
    const client = new FetchRouterClient("http://router.local");
    const result = await client.mergeSession("s1", "main");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/merge",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ target_branch: "main" }),
      }),
    );
    expect(result.conflicted).toBe(false);
  });

  it("getEvents GETs /events with session_id, since, and types query params, no Accept header", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getEvents({ sessionId: "s1", since: 6, types: ["pipeline.started", "stage.started"] });
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/events?session_id=s1&since=6&types=pipeline.started%2Cstage.started",
    );
    expect(fetchMock.mock.calls[0][1]).toBeUndefined();
  });

  it("getEvents omits since and types when absent", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.getEvents({ sessionId: "s1" });
    expect(fetchMock).toHaveBeenCalledWith("http://router.local/events?session_id=s1");
  });

  it("metricsPipelineRuns omits session_id when absent", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.metricsPipelineRuns();
    expect(fetchMock).toHaveBeenCalledWith("http://router.local/metrics/pipelines");
  });

  it("metricsPipelineRuns includes session_id when given", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.metricsPipelineRuns("s1");
    expect(fetchMock).toHaveBeenCalledWith("http://router.local/metrics/pipelines?session_id=s1");
  });
});

describe("FetchRouterClient — typed methods built on the private transport helpers", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("listTasks omits the status query param when absent", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    const tasks = await client.listTasks("s1");
    expect(fetchMock).toHaveBeenCalledWith("http://router.local/tasks?session_id=s1");
    expect(tasks).toEqual([]);
  });

  it("listTasks appends the status query param when given", async () => {
    const fetchMock = mockFetchOnce([]);
    const client = new FetchRouterClient("http://router.local");
    await client.listTasks("s1", "ready");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/tasks?session_id=s1&status=ready",
    );
  });

  it("listSessions throws on a non-ok response", async () => {
    mockFetchOnce(undefined, false, 500);
    const client = new FetchRouterClient("http://router.local");
    await expect(client.listSessions()).rejects.toThrow(/500/);
  });

  it("createSession POSTs a JSON body and decodes the response", async () => {
    const fetchMock = mockFetchOnce({ session: { id: "s1" }, addr: "localhost:1" });
    const client = new FetchRouterClient("http://router.local");
    const result = await client.createSession({ name: "s1" });
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ name: "s1" }),
      }),
    );
    expect(result.session.id).toBe("s1");
  });

  it("interrupt returns undefined for a 204 response", async () => {
    mockFetchOnce(undefined, true, 204);
    const client = new FetchRouterClient("http://router.local");
    const result = await client.interrupt("s1");
    expect(result).toBeUndefined();
  });

  it("closeSession POSTs to /sessions/:id/terminate", async () => {
    const fetchMock = mockFetchOnce(undefined, true, 204);
    const client = new FetchRouterClient("http://router.local");
    const result = await client.closeSession("s1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/terminate",
      expect.objectContaining({ method: "POST" }),
    );
    expect(result).toBeUndefined();
  });

  it("resumeSession POSTs to /sessions/:id/resume and decodes the addr", async () => {
    const fetchMock = mockFetchOnce({ addr: "localhost:2" });
    const client = new FetchRouterClient("http://router.local");
    const result = await client.resumeSession("s1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1/resume",
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.addr).toBe("localhost:2");
  });

  it("deleteSession DELETEs /sessions/:id", async () => {
    const fetchMock = mockFetchOnce(undefined, true, 204);
    const client = new FetchRouterClient("http://router.local");
    const result = await client.deleteSession("s1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/sessions/s1",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(result).toBeUndefined();
  });

  it("validateTemplate throws RouterValidationError on a {errors:[...]} failure body", async () => {
    const fetchMock = mockFetchOnce(
      { errors: [{ field: "source", message: "duplicate stage name" }] },
      false,
      400,
    );
    const client = new FetchRouterClient("http://router.local");
    await expect(client.validateTemplate("bad")).rejects.toThrow(RouterValidationError);
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/templates/validate",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ source: "bad" }),
      }),
    );
  });

  it("updateAgent PUTs a JSON body and decodes the response", async () => {
    const fetchMock = mockFetchOnce({ id: "a1" });
    const client = new FetchRouterClient("http://router.local");
    const agent: Agent = {
      id: "a1",
      name: "a1",
      prompt: "do the thing",
      created_at: "2024-01-01T00:00:00Z",
      updated_at: "2024-01-01T00:00:00Z",
    };
    const result = await client.updateAgent(agent);
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/agents/a1",
      expect.objectContaining({ method: "PUT", body: JSON.stringify(agent) }),
    );
    expect(result).toEqual({ id: "a1" });
  });

  it("deleteAgent DELETEs and decodes a 200 body", async () => {
    const fetchMock = mockFetchOnce({ status: "deleted" });
    const client = new FetchRouterClient("http://router.local");
    const result = await client.deleteAgent("a1");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://router.local/agents/a1",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(result).toEqual({ status: "deleted" });
  });
});
