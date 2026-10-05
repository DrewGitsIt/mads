import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConversationImportStep } from "./ConversationImportStep";
import type { ConversationImportScan } from "../../router/types";

type Route = unknown | (() => Response);

function routedFetch(routes: Record<string, Route>) {
  return vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const path = new URL(input.toString()).pathname;
    const key = `${method} ${path}`;
    const body = routes[key];
    if (body === undefined) throw new Error(`unstubbed route: ${key}`);
    if (typeof body === "function") return Promise.resolve((body as () => Response)());
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
}

function callsTo(fetchMock: ReturnType<typeof vi.fn>, key: string) {
  return fetchMock.mock.calls.filter((args: unknown[]) => {
    const [url, init] = args as [string | URL, RequestInit?];
    return `${init?.method ?? "GET"} ${new URL(url.toString()).pathname}` === key;
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

const empty: ConversationImportScan = {
  providers: [],
  folders: [],
  total: 0,
  already_imported: 0,
  looked_for: ["/home/dev/.claude/projects"],
};

const serverError = () => new Response("boom", { status: 500 });

describe("ConversationImportStep", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the counts, folders and what it looked at, and imports everything on Import", async () => {
    const fetchMock = routedFetch({
      "GET /imports/conversations": found,
      "POST /imports/conversations": { imported: 5, skipped_existing: 0 },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    render(<ConversationImportStep onNext={onNext} onBack={vi.fn()} />);

    await waitFor(() =>
      expect(screen.getByText("Found 5 conversations across 2 folders from Claude Code.")).toBeTruthy(),
    );
    expect(screen.getByText("/home/dev/work/app")).toBeTruthy();
    expect(screen.getByText("folder no longer exists")).toBeTruthy();
    expect(screen.getByText(/nothing is uploaded or copied anywhere/i)).toBeTruthy();
    fireEvent.click(screen.getByText("What does mads look for?"));
    expect(screen.getByText("/home/dev/.claude/projects")).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Import conversations" }));
    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(callsTo(fetchMock, "POST /imports/conversations")).toHaveLength(1);
  });

  it("skips without importing anything", async () => {
    const fetchMock = routedFetch({ "GET /imports/conversations": found });
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    render(<ConversationImportStep onNext={onNext} onBack={vi.fn()} />);

    await waitFor(() => expect(screen.getByText("Skip for now")).toBeTruthy());
    fireEvent.click(screen.getByText("Skip for now"));

    expect(onNext).toHaveBeenCalledTimes(1);
    expect(callsTo(fetchMock, "POST /imports/conversations")).toHaveLength(0);
  });

  it("shows an honest empty state with a single Continue and no error", async () => {
    vi.stubGlobal("fetch", routedFetch({ "GET /imports/conversations": empty }));
    const onNext = vi.fn();
    render(<ConversationImportStep onNext={onNext} onBack={vi.fn()} />);

    await waitFor(() => expect(screen.getByText(/nothing to import/i)).toBeTruthy());
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("button", { name: "Import conversations" })).toBeNull();
    expect(screen.queryByText("Skip for now")).toBeNull();
    fireEvent.click(screen.getByText("What does mads look for?"));
    expect(screen.getByText("/home/dev/.claude/projects")).toBeTruthy();
    fireEvent.click(screen.getByText("Continue"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });

  it("keeps the user moving when the import fails, and lets them retry", async () => {
    let attempts = 0;
    const fetchMock = routedFetch({
      "GET /imports/conversations": found,
      "POST /imports/conversations": () => {
        attempts += 1;
        return attempts === 1
          ? serverError()
          : new Response(JSON.stringify({ imported: 5, skipped_existing: 0 }), { status: 200 });
      },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    render(<ConversationImportStep onNext={onNext} onBack={vi.fn()} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Import conversations" })).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Import conversations" }));

    await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/couldn.t import conversations/i));
    expect(onNext).not.toHaveBeenCalled();
    // Still offered: Skip, and Import again.
    expect(screen.getByText("Skip for now")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Import conversations" }));
    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(attempts).toBe(2);
  });

  it("shows a readable error and a Continue when the scan fails", async () => {
    vi.stubGlobal("fetch", routedFetch({ "GET /imports/conversations": serverError }));
    const onNext = vi.fn();
    render(<ConversationImportStep onNext={onNext} onBack={vi.fn()} />);

    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toMatch(/couldn.t look for existing conversations/i),
    );
    expect(screen.queryByRole("button", { name: "Import conversations" })).toBeNull();
    fireEvent.click(screen.getByText("Continue"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });
});

