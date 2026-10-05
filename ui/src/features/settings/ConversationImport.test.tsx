import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConversationImport } from "./ConversationImport";
import type { ConversationImportScan } from "../../router/types";

type Route = unknown | (() => Response);

function routedFetch(routes: Record<string, Route>) {
  return vi
    .fn()
    .mockImplementation((input: URL | string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const path = new URL(input.toString()).pathname;
      const key = `${method} ${path}`;
      const body = routes[key];
      if (body === undefined) throw new Error(`unstubbed route: ${key}`);
      if (typeof body === "function")
        return Promise.resolve((body as () => Response)());
      return Promise.resolve(
        new Response(JSON.stringify(body), { status: 200 }),
      );
    });
}

function callsTo(fetchMock: ReturnType<typeof vi.fn>, key: string) {
  return fetchMock.mock.calls.filter((args: unknown[]) => {
    const [url, init] = args as [string | URL, RequestInit?];
    return (
      `${init?.method ?? "GET"} ${new URL(url.toString()).pathname}` === key
    );
  });
}

const found: ConversationImportScan = {
  providers: [{ provider: "claude-code", count: 5 }],
  folders: [
    { dir: "/home/dev/work/app", count: 3, exists: true },
    { dir: "/home/dev/old-experiment", count: 2, exists: false },
  ],
  total: 5,
  already_imported: 1,
  looked_for: ["/home/dev/.claude/projects"],
};

const nothingNew: ConversationImportScan = {
  providers: [],
  folders: [],
  total: 0,
  already_imported: 4,
  looked_for: ["/home/dev/.claude/projects"],
};

describe("ConversationImport (Settings > General)", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("reads nothing until the user asks", () => {
    const fetchMock = routedFetch({});
    vi.stubGlobal("fetch", fetchMock);
    render(<ConversationImport />);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: "Scan for conversations" }),
    ).toBeTruthy();
  });

  it("scans, summarizes in one sentence, and imports everything found", async () => {
    const fetchMock = routedFetch({
      "GET /imports/conversations": found,
      "POST /imports/conversations": { imported: 5, skipped_existing: 1 },
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<ConversationImport />);

    fireEvent.click(
      screen.getByRole("button", { name: "Scan for conversations" }),
    );
    expect(
      await screen.findByText(
        "Found 5 conversations across 2 folders from Claude Code (1 folder no longer exists).",
      ),
    ).toBeTruthy();
    expect(screen.getByText("/home/dev/.claude/projects")).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();

    fireEvent.click(
      screen.getByRole("button", { name: "Import 5 conversations" }),
    );
    expect((await screen.findByRole("status")).textContent).toContain(
      "Imported 5 conversations.",
    );
    expect(callsTo(fetchMock, "POST /imports/conversations")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /^Import \d/ })).toBeNull();
  });

  it("says when there is nothing new because mads already has it all", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({ "GET /imports/conversations": nothingNew }),
    );
    render(<ConversationImport />);
    fireEvent.click(
      screen.getByRole("button", { name: "Scan for conversations" }),
    );
    expect(
      await screen.findByText(
        "Nothing new to import — mads already has all 4 conversations it found.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Import/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Scan again" })).toBeTruthy();
  });

  it("shows a failed import and lets the user retry", async () => {
    let attempts = 0;
    const fetchMock = routedFetch({
      "GET /imports/conversations": found,
      "POST /imports/conversations": () => {
        attempts++;
        return attempts === 1
          ? new Response(
              JSON.stringify({ error: "data service unavailable" }),
              { status: 503 },
            )
          : new Response(JSON.stringify({ imported: 5, skipped_existing: 0 }), {
              status: 200,
            });
      },
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<ConversationImport />);
    fireEvent.click(
      screen.getByRole("button", { name: "Scan for conversations" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Import 5 conversations" }),
    );
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Couldn't import conversations",
    );

    fireEvent.click(
      screen.getByRole("button", { name: "Import 5 conversations" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toContain(
        "Imported 5 conversations.",
      ),
    );
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a failed scan without trapping the user", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /imports/conversations": () =>
          new Response(JSON.stringify({ error: "boom" }), { status: 500 }),
      }),
    );
    render(<ConversationImport />);
    fireEvent.click(
      screen.getByRole("button", { name: "Scan for conversations" }),
    );
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Couldn't look for existing conversations",
    );
    expect(
      (
        screen.getByRole("button", {
          name: "Scan for conversations",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);
  });
});
