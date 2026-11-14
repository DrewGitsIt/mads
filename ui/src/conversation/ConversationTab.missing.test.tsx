// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// The empty pane when the agent's store no longer holds the conversation
// (TranscriptPage.missing): said only for a session that has had a turn.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { page } from "./keyed/testFrames";
import type { Session, TranscriptMissing } from "../router/types";

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

const RETENTION = { file: "/Users/d/.claude/settings.json", setting: "cleanupPeriodDays", default_days: 30 };

async function mount(missing: TranscriptMissing, session: Partial<Session> = {}) {
  const client = new FakeRouterClient();
  const record = await client.getSession("session-active");
  vi.spyOn(client, "getSession").mockResolvedValue({ ...record, provider: "claude-code", ...session });
  vi.spyOn(client, "getTranscriptPage").mockResolvedValue({ ...page([]), missing });
  render(<ConversationTab sessionId="session-active" routerClient={client} />);
  await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
  return client;
}

describe("ConversationTab — transcript missing", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    delete window.mads;
  });

  it("names the agent's retention setting and opens its settings file", async () => {
    const openFile = vi.fn().mockResolvedValue("opened");
    window.mads = { links: { open: vi.fn(), openFile, describe: vi.fn() } };
    await mount({ retention: RETENTION });

    const note = await screen.findByTestId("transcript-missing");
    expect(note.textContent).toContain(
      "This conversation's transcript is no longer on disk. Claude Code deletes transcripts older than its cleanupPeriodDays setting (30 days by default).",
    );
    expect(note.querySelector("code")?.textContent).toBe("cleanupPeriodDays");
    expect(screen.queryByText("No conversation yet")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Open Claude Code settings" }));
    await waitFor(() => expect(openFile).toHaveBeenCalledWith(RETENTION.file));
  });

  it("says the settings file isn't there when opening finds it missing", async () => {
    window.mads = { links: { open: vi.fn(), openFile: vi.fn().mockResolvedValue("missing"), describe: vi.fn() } };
    await mount({ retention: RETENTION });
    fireEvent.click(await screen.findByRole("button", { name: "Open Claude Code settings" }));
    const button = await screen.findByRole("button", { name: "Not on disk" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });

  it("leaves out the default and the button outside Electron", async () => {
    await mount({ retention: { file: RETENTION.file, setting: "cleanupPeriodDays" } }, { provider: "hermes" });
    const note = await screen.findByTestId("transcript-missing");
    expect(note.textContent).toContain("Hermes deletes transcripts older than its cleanupPeriodDays setting.");
    expect(screen.queryByRole("button", { name: /settings/ })).toBeNull();
  });

  it("says only that it's gone for an agent without a retention setting", async () => {
    window.mads = { links: { open: vi.fn(), openFile: vi.fn(), describe: vi.fn() } };
    await mount({}, { provider: "codex" });
    const note = await screen.findByTestId("transcript-missing");
    expect(note.textContent).toBe("This conversation's transcript is no longer on disk.");
    expect(screen.queryByRole("button", { name: /settings/ })).toBeNull();
  });

  it("keeps the plain empty state for a session that hasn't had a turn", async () => {
    await mount({ retention: RETENTION }, { last_turn_at: undefined, last_usage: undefined });
    await waitFor(() => expect(screen.getByText("No conversation yet")).toBeTruthy());
    expect(screen.queryByTestId("transcript-missing")).toBeNull();
  });
});
