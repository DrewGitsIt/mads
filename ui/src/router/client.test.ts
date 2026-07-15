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
});
