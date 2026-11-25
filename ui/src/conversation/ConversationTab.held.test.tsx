// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// Held link updates (docs/HELD-UPDATES-DESIGN.md) in a sleeping session's
// conversation: the list at the end of the stream, the composer's chip,
// discard with Undo, and Send updates only.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { page } from "./keyed/testFrames";
import type { HeldUpdate } from "../router/types";

vi.mock("@monaco-editor/react", () => ({
  Editor: () => <textarea data-testid="fake-monaco-editor" />,
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

function heldUpdate(id: number, source: string): HeldUpdate {
  return {
    id,
    link_id: "lnk_1",
    source_session_id: source,
    source_name: source,
    kind: "digest",
    text: `update ${id}`,
    created_at: `2026-11-05T13:5${id}:00Z`,
    noop: true,
  };
}

async function mountSleeping() {
  const client = new FakeRouterClient();
  const session = await client.getSession("session-inactive");
  session.waiting_updates = 2;
  client.heldUpdates.set("session-inactive", {
    held: [heldUpdate(1, "Pipeline study"), heldUpdate(2, "Pipeline study")],
    discarded: [],
  });
  vi.spyOn(client, "getTranscriptPage").mockResolvedValue(
    page([
      { role: "user", key: "u0", text: "kick it off", timestamp: "2026-11-05T13:00:00Z" },
      { role: "agent", key: "a0", text: "Started.", timestamp: "2026-11-05T13:00:05Z" },
    ]),
  );
  render(<ConversationTab sessionId="session-inactive" routerClient={client} />);
  const list = await screen.findByRole("region", { name: "Held updates" });
  return { client, list };
}

describe("ConversationTab — held updates", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists them at the end of the stream and shows them on the composer", async () => {
    const { list } = await mountSleeping();
    const rows = document.querySelector("[data-chat-rows]")!;
    expect(rows.lastElementChild).toBe(list);
    expect(within(list).getByText("2 updates held")).toBeTruthy();
    expect(screen.getByTestId("held-chip").textContent).toBe("⌗ 2 updates · Pipeline study×");
  });

  it("discards a row with an Undo toast that brings it back", async () => {
    const { client, list } = await mountSleeping();
    const discard = vi.spyOn(client, "discardHeldUpdates");
    fireEvent.click(within(list).getAllByRole("button", { name: "Discard update from Pipeline study" })[0]);
    expect(discard).toHaveBeenCalledWith([2]);
    await waitFor(() => expect(within(list).getByText("1 update held")).toBeTruthy());
    expect(within(list).getByText("1 discarded · Pipeline study")).toBeTruthy();
    const toast = screen.getByRole("status");
    expect(within(toast).getByText("Discarded 1 update")).toBeTruthy();

    fireEvent.click(within(toast).getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(within(list).getByText("2 updates held")).toBeTruthy());
    expect(within(list).queryByText(/discarded/)).toBeNull();
  });

  it("discards them all from the composer chip", async () => {
    const { client } = await mountSleeping();
    const discard = vi.spyOn(client, "discardHeldUpdates");
    fireEvent.click(screen.getByRole("button", { name: "Discard all 2 updates" }));
    expect(discard).toHaveBeenCalledWith([1, 2]);
    await waitFor(() => expect(screen.queryByTestId("held-chip")).toBeNull());
    expect(screen.getByText("Discarded 2 updates")).toBeTruthy();
    expect(screen.getByText("2 discarded · Pipeline study")).toBeTruthy();
  });

  it("Send updates only delivers them as a turn of their own", async () => {
    const { client, list } = await mountSleeping();
    fireEvent.click(within(list).getByRole("button", { name: "Send updates only" }));
    await waitFor(() => expect(client.deliveredHeld).toEqual(["session-inactive"]));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Held updates" })).toBeNull());
  });

  it("says why when Send updates only couldn't wake the session", async () => {
    const { client, list } = await mountSleeping();
    vi.spyOn(client, "deliverHeldUpdates").mockRejectedValue(new Error("provider not installed"));
    fireEvent.click(within(list).getByRole("button", { name: "Send updates only" }));
    expect(await screen.findByText("Couldn't send the updates")).toBeTruthy();
    expect(screen.getByText("provider not installed")).toBeTruthy();
    expect(within(list).getByText("2 updates held")).toBeTruthy();
  });
});
