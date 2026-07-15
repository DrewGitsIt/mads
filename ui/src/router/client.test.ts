import { afterEach, describe, expect, it, vi } from "vitest";
import { FetchRouterClient } from "./client";

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

  it("streamControlEvents opens /events with session_id and types filters", () => {
    // happy-dom (this suite's test environment) doesn't implement
    // EventSource, so stub the global the same way mockFetchOnce stubs
    // fetch — this only asserts the URL construction, not real SSE framing.
    class StubEventSource {
      constructor(public url: string) {}
      close(): void {}
    }
    vi.stubGlobal("EventSource", StubEventSource);

    const client = new FetchRouterClient("http://router.local");
    const source = client.streamControlEvents({
      sessionId: "s1",
      types: ["merge.attempted", "merge.completed"],
    });
    expect((source as unknown as StubEventSource).url).toBe(
      "http://router.local/events?session_id=s1&types=merge.attempted%2Cmerge.completed",
    );
  });
});
