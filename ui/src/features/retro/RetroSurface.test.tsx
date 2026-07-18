import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RetroSurface } from "./RetroSurface";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { fixtureRetro, fixtureSessions } from "../../router/fixtures";

let nextSeq = 1;
function envelope(type: string, payload: unknown) {
  return JSON.stringify({ seq: nextSeq++, type, actor: "system", payload });
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

describe("RetroSurface", () => {
  beforeEach(() => {
    nextSeq = 1;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("full round-trip: generate -> 3-step progress -> DPA cards render", async () => {
    const sessions = [fixtureSessions["session-active"], fixtureSessions["session-inactive"]];
    const fetchMock = vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
      const url = input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/sessions")) return Promise.resolve(jsonResponse(sessions));
      if (url.endsWith("/retros") && method === "POST") {
        return Promise.resolve(jsonResponse({ job_id: "job-1", retro_id: "retro-1" }, 202));
      }
      if (url.endsWith("/retros")) return Promise.resolve(jsonResponse([]));
      if (url.includes("/retros/retro-1") && method === "GET") {
        return Promise.resolve(jsonResponse(fixtureRetro));
      }
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<RetroSurface />);

    await waitFor(() => expect(screen.getByLabelText(sessions[0].name)).toBeTruthy());

    fireEvent.click(screen.getByLabelText(sessions[0].name));
    fireEvent.click(screen.getByLabelText(sessions[1].name));
    fireEvent.click(screen.getByText("Generate retro"));

    await waitFor(() => expect(screen.queryByText("Generate retro")).toBeNull());
    expect(screen.getByRole("status", { name: "Retro generation progress" })).toBeTruthy();

    const source = FakeEventSource.instances.find((i) => i.url.includes("retro.started"))!;
    act(() => source.emit("retro.started", envelope("retro.started", { retro_id: "retro-1" })));
    expect(screen.getByText("Starting").className).toMatch(/active/);

    act(() =>
      source.emit(
        "retro.progress",
        envelope("retro.progress", { retro_id: "retro-1", step: "gathering" }),
      ),
    );
    expect(screen.getByText("Gathering").className).toMatch(/active/);
    expect(screen.getByText("Starting").className).toMatch(/done/);

    act(() =>
      source.emit(
        "retro.progress",
        envelope("retro.progress", { retro_id: "retro-1", step: "generating" }),
      ),
    );
    expect(screen.getByText("Generating").className).toMatch(/active/);
    expect(screen.getByText("Gathering").className).toMatch(/done/);

    act(() => source.emit("retro.completed", envelope("retro.completed", { retro_id: "retro-1" })));

    await waitFor(() => expect(screen.getByText(fixtureRetro.dpas[0].problem)).toBeTruthy());
    expect(screen.getByText(fixtureRetro.dpas[1].problem)).toBeTruthy();
    expect(screen.getByText(fixtureRetro.dpas[2].problem)).toBeTruthy();
  });

  it("chip-toggle: rejecting an accepted DPA fires POST reject and decrements the follow-up CTA count", async () => {
    const sessions = [fixtureSessions["session-active"], fixtureSessions["session-inactive"]];
    const fetchMock = vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
      const url = input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/sessions")) return Promise.resolve(jsonResponse(sessions));
      if (url.endsWith("/retros")) return Promise.resolve(jsonResponse([fixtureRetro]));
      if (url.includes(`/retros/${fixtureRetro.id}/dpas/dpa_1/reject`) && method === "POST") {
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      if (url.includes(`/retros/${fixtureRetro.id}`) && method === "GET") {
        return Promise.resolve(jsonResponse(fixtureRetro));
      }
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<RetroSurface />);

    await waitFor(() => expect(screen.getByText("3 DPAs")).toBeTruthy());
    fireEvent.click(screen.getByText("3 DPAs"));

    await waitFor(() => expect(screen.getByText(fixtureRetro.dpas[0].problem)).toBeTruthy());
    expect(screen.getByText("Create follow-up session (2 accepted)")).toBeTruthy();

    // dpa_1 is the first "Accepted" chip in the list (dpa_1, dpa_3 both start accepted).
    fireEvent.click(screen.getAllByText("Accepted")[0]);

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([reqInput, reqInit]) =>
            reqInput.toString().includes("/dpas/dpa_1/reject") &&
            (reqInit as RequestInit | undefined)?.method === "POST",
        ),
      ).toBe(true),
    );
    expect(screen.getByText("Create follow-up session (1 accepted)")).toBeTruthy();
  });

  it("follow-up CTA: creating a follow-up session surfaces the created session/tasks", async () => {
    const sessions = [fixtureSessions["session-active"], fixtureSessions["session-inactive"]];
    const fetchMock = vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
      const url = input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/sessions")) return Promise.resolve(jsonResponse(sessions));
      if (url.endsWith("/retros")) return Promise.resolve(jsonResponse([fixtureRetro]));
      if (url.includes(`/retros/${fixtureRetro.id}/follow-up-session`) && method === "POST") {
        return Promise.resolve(
          jsonResponse(
            {
              session: {
                id: "session-followup",
                name: "Follow-up",
                activity: "active",
                created_at: "2026-07-15T00:00:00Z",
                updated_at: "2026-07-15T00:00:00Z",
              },
              addr: "localhost:0",
              tasks: [{ id: "t1" }, { id: "t2" }],
            },
            201,
          ),
        );
      }
      if (url.includes(`/retros/${fixtureRetro.id}`) && method === "GET") {
        return Promise.resolve(jsonResponse(fixtureRetro));
      }
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<RetroSurface />);

    await waitFor(() => expect(screen.getByText("3 DPAs")).toBeTruthy());
    fireEvent.click(screen.getByText("3 DPAs"));

    await waitFor(() => expect(screen.getByText("Create follow-up session (2 accepted)")).toBeTruthy());
    fireEvent.click(screen.getByText("Create follow-up session (2 accepted)"));

    await waitFor(() => expect(screen.getByText("Follow-up session created")).toBeTruthy());
    expect(screen.getByText("2 tasks imported")).toBeTruthy();
  });

  it("cancel mid-generation leaves the picker restored with the same selection and notes", async () => {
    const sessions = [fixtureSessions["session-active"], fixtureSessions["session-inactive"]];
    const fetchMock = vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
      const url = input.toString();
      const method = init?.method ?? "GET";
      if (url.includes("/sessions")) return Promise.resolve(jsonResponse(sessions));
      if (url.endsWith("/retros") && method === "POST") {
        return Promise.resolve(jsonResponse({ job_id: "job-1", retro_id: "retro-1" }, 202));
      }
      if (url.endsWith("/retros")) return Promise.resolve(jsonResponse([]));
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<RetroSurface />);

    await waitFor(() => expect(screen.getByLabelText(sessions[0].name)).toBeTruthy());

    fireEvent.click(screen.getByLabelText(sessions[0].name));
    fireEvent.click(screen.getByLabelText(sessions[1].name));
    fireEvent.change(screen.getByLabelText("Observations"), {
      target: { value: "watch the retries" },
    });
    fireEvent.click(screen.getByText("Generate retro"));

    await waitFor(() =>
      expect(screen.getByRole("status", { name: "Retro generation progress" })).toBeTruthy(),
    );

    const source = FakeEventSource.instances.find((i) => i.url.includes("retro.started"))!;
    act(() => source.emit("retro.started", envelope("retro.started", { retro_id: "retro-1" })));

    fireEvent.click(screen.getByText("Cancel"));

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([reqInput, reqInit]) =>
            reqInput.toString().includes("/jobs/job-1/cancel") &&
            (reqInit as RequestInit | undefined)?.method === "POST",
        ),
      ).toBe(true),
    );

    act(() => source.emit("retro.cancelled", envelope("retro.cancelled", { retro_id: "retro-1" })));

    await waitFor(() => expect(screen.getByText("Generate retro")).toBeTruthy());
    expect((screen.getByLabelText(sessions[0].name) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText(sessions[1].name) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Observations") as HTMLTextAreaElement).value).toBe(
      "watch the retries",
    );
  });
});
