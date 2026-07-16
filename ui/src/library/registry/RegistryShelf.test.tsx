import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RegistryShelf } from "./RegistryShelf";
import { FakeEventSource } from "../../router/testFakeEventSource";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

// Single global `fetch` mock dispatching by path, since the shelf fans out
// to /registry/index plus /agents, /capabilities, /templates (for
// installed-state) in parallel.
function stubFetch(overrides: {
  index?: unknown[];
  agents?: unknown[];
  capabilities?: unknown[];
  templates?: unknown[];
}) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/registry/index")) return Promise.resolve(jsonResponse(overrides.index ?? []));
      if (url.includes("/registry/install") || url.includes("/registry/update")) {
        return Promise.resolve(jsonResponse({ job_id: "j1" }, 202));
      }
      if (url.includes("/agents")) return Promise.resolve(jsonResponse(overrides.agents ?? []));
      if (url.includes("/capabilities")) return Promise.resolve(jsonResponse(overrides.capabilities ?? []));
      if (url.includes("/templates")) return Promise.resolve(jsonResponse(overrides.templates ?? []));
      return Promise.resolve(jsonResponse([]));
    }),
  );
}

function jobEventSource() {
  const source = FakeEventSource.instances.find((s) => s.url.includes("job.completed"));
  if (!source) throw new Error("no job.completed EventSource subscribed");
  return source;
}

describe("RegistryShelf", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the browse list from the registry index", async () => {
    stubFetch({
      index: [
        {
          type: "agent",
          name: "diff-reviewer",
          description: "Reviews diffs",
          versions: ["1"],
          latest: "1",
        },
      ],
    });
    render(<RegistryShelf />);
    await waitFor(() => expect(screen.getByText("diff-reviewer")).toBeTruthy());
    expect(screen.getByText("Reviews diffs")).toBeTruthy();
    expect(screen.getByText("Install")).toBeTruthy();
  });

  it("shows Update instead of Install for an entry already installed locally", async () => {
    stubFetch({
      index: [
        { type: "agent", name: "diff-reviewer", versions: ["1", "2"], latest: "2" },
      ],
      agents: [
        {
          id: "a1",
          name: "diff-reviewer",
          prompt: "p",
          created_at: "t",
          updated_at: "t",
        },
      ],
    });
    render(<RegistryShelf />);
    await waitFor(() => expect(screen.getByText("Update")).toBeTruthy());
  });

  it("installs an entry and clears pending on job.completed", async () => {
    stubFetch({
      index: [{ type: "agent", name: "diff-reviewer", versions: ["1"], latest: "1" }],
    });
    render(<RegistryShelf />);
    await waitFor(() => expect(screen.getByText("diff-reviewer")).toBeTruthy());

    fireEvent.click(screen.getByText("Install"));
    await waitFor(() =>
      expect((screen.getByText("Install") as HTMLButtonElement).disabled).toBe(true),
    );

    jobEventSource().emit(
      "job.completed",
      JSON.stringify({
        seq: 1,
        type: "job.completed",
        actor: "system",
        payload: { job_id: "j1" },
      }),
    );

    await waitFor(() =>
      expect((screen.getByText("Install") as HTMLButtonElement).disabled).toBe(false),
    );
  });

  it("surfaces the job error on job.failed", async () => {
    stubFetch({
      index: [{ type: "agent", name: "diff-reviewer", versions: ["1"], latest: "1" }],
    });
    render(<RegistryShelf />);
    await waitFor(() => expect(screen.getByText("diff-reviewer")).toBeTruthy());

    fireEvent.click(screen.getByText("Install"));
    await waitFor(() => jobEventSource());

    jobEventSource().emit(
      "job.failed",
      JSON.stringify({
        seq: 1,
        type: "job.failed",
        actor: "system",
        payload: { job_id: "j1", error: "malformed bundle" },
      }),
    );

    await waitFor(() => expect(screen.getByText("malformed bundle")).toBeTruthy());
  });

  it("opens the Publish dialog from the header button", async () => {
    stubFetch({ index: [] });
    render(<RegistryShelf />);
    await waitFor(() => expect(screen.getByText("No matching items.")).toBeTruthy());

    fireEvent.click(screen.getByText("Publish"));
    expect(screen.getByText("Publish to registry")).toBeTruthy();
  });
});
