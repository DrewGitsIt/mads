// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// The tab's session-record refetch: the bus replays its durable log on
// connect, and one GET /sessions/:id per replayed event (245 link events →
// ~250 reads from this tab alone) is what it used to cost.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, within } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureIsolatedSession } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Session, TranscriptMessage } from "../router/types";

vi.mock("@monaco-editor/react", () => ({
  Editor: ({ value }: { value?: string }) => (
    <textarea data-testid="fake-monaco-editor" value={value ?? ""} readOnly />
  ),
}));

if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList;
}

if (typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

const SID = fixtureIsolatedSession.id;
const transcript: TranscriptMessage[] = [
  { role: "user", text: "Fix the deploy script", timestamp: "2026-10-05T14:00:00Z" },
  { role: "agent", text: "Done — the detection works on both runners.", timestamp: "2026-10-05T14:01:00Z" },
];

// The bus de-dupes by seq per type for the whole file: keep them unique.
let seq = 6_000_000;
function emitSessionUpdated() {
  const source = FakeEventSource.instances.filter((s) => s.url.includes("session.updated")).at(-1);
  expect(source).toBeTruthy();
  seq += 1;
  source!.emit(
    "session.updated",
    JSON.stringify({ seq, type: "session.updated", timestamp: "2026-10-05T14:05:00Z", actor: "router", session_id: SID }),
  );
}

const quiet = (ms: number) => act(() => new Promise<void>((r) => setTimeout(r, ms)));

function makeClient(session: Session = fixtureIsolatedSession) {
  return new FakeRouterClient({ [SID]: session }, { [SID]: transcript });
}

async function mount(client: FakeRouterClient) {
  render(<ConversationTab sessionId={SID} routerClient={client} />);
  await screen.findByText("Done — the detection works on both runners.");
}

const tabReads = (spy: { mock: { calls: unknown[][] } }) => spy.mock.calls.filter(([id]) => id === SID).length;

describe("ConversationTab — session refetch", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("collapses a burst of 200 session.updated for the open session into at most 2 GET /sessions/:id", async () => {
    const client = makeClient();
    const getSession = vi.spyOn(client, "getSession");
    await mount(client);
    await quiet(100);
    const before = tabReads(getSession);

    act(() => {
      for (let i = 0; i < 200; i++) emitSessionUpdated();
    });
    await quiet(700);
    const reads = tabReads(getSession) - before;
    expect(reads).toBeGreaterThanOrEqual(1);
    expect(reads).toBeLessThanOrEqual(2);
  });

  it("an event-driven refetch still lands the new record", async () => {
    const client = makeClient({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0, unpushed_count: 0 });
    const getSession = vi.spyOn(client, "getSession");
    await mount(client);
    await quiet(100);
    expect(screen.getByTestId("dock-status").querySelector('[data-work-state="push"]')).toBeNull();

    getSession.mockResolvedValue({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0, unpushed_count: 2 });
    act(() => emitSessionUpdated());
    await waitFor(() =>
      expect(screen.getByTestId("dock-status").querySelector('[data-work-state="push"]')).toBeTruthy(),
    );
  });

  it("the refetch after a push is immediate, not debounced", async () => {
    const client = makeClient({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0, unpushed_count: 2 });
    const getSession = vi.spyOn(client, "getSession");
    await mount(client);
    await quiet(100);
    fireEvent.click(screen.getByTestId("dock-status").querySelector<HTMLButtonElement>('[data-work-state="push"]')!);
    const dialog = await screen.findByRole("alertdialog", { name: "Push mads/session/session-isolated to origin?" });
    const before = tabReads(getSession);

    fireEvent.click(within(dialog).getByRole("button", { name: "Push" }));
    // onPushed raises the toast and refetches in the same call: by the time
    // the toast renders, the read has already gone out (a debounced one
    // would still be 50 ms away).
    await screen.findByText("Pushed 2 commits to origin/mads/session/session-isolated");
    expect(tabReads(getSession)).toBe(before + 1);
  });
});
