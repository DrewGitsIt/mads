// @vitest-environment jsdom
// ChatRenderer/SessionSummaryCard render markdown through DOMPurify, which
// doesn't sanitize correctly under happy-dom (see ChatRenderer/markdown.test.tsx).
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { TransportSwitchError } from "../router/client";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";

// jsdom has no matchMedia; xterm.js needs it to mount. happy-dom has one,
// but this file needs jsdom for DOMPurify (see comment above).
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

function makeStubBridge(): PtyBridge {
  return {
    spawn: vi.fn(async () => "pty-1"),
    write: vi.fn(),
    resize: vi.fn(),
    kill: vi.fn(),
    onData: vi.fn(() => () => {}),
    onExit: vi.fn(() => () => {}),
  };
}

describe("ConversationTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the chat display mode by default", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    expect(screen.getByText(/Add retry backoff to the ingest worker, please/)).toBeTruthy();
  });

  // Terminal display mode now drives the session's own transport
  // (SessionTerminalBridge, backed by switchTransport/streamSession), not the
  // Electron ptyBridge prop — that prop is ShellDock-only now (see
  // ShellDock.test.tsx for its own spawn coverage).
  it("switches to terminal display mode using the session's own transport, not the Electron pty bridge", async () => {
    const client = new FakeRouterClient();
    const bridge = makeStubBridge();
    render(
      <ConversationTab sessionId="session-active" routerClient={client} ptyBridge={bridge} />,
    );

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Terminal"));

    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
    expect(bridge.spawn).not.toHaveBeenCalled();
  });

  it("shows a switching indicator and disables the composer while the transport switch is pending", async () => {
    const client = new FakeRouterClient();
    let resolveSwitch!: (v: { mode: string; ref: string }) => void;
    vi.spyOn(client, "switchTransport").mockReturnValueOnce(
      new Promise((resolve) => {
        resolveSwitch = resolve;
      }),
    );
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Terminal"));

    await waitFor(() => expect(screen.getByText(/Switching to terminal…/)).toBeTruthy());
    // History stays visible and read-only — never a blank pane mid-switch.
    expect(screen.getByText("Add retry backoff to the ingest worker, please.")).toBeTruthy();
    const sendButton = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    expect(sendButton.disabled).toBe(true);

    resolveSwitch({ mode: "terminal", ref: "test-ref" });
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
  });

  it("shows a busy failure with an interrupt-and-switch escape hatch", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "switchTransport")
      .mockRejectedValueOnce(new TransportSwitchError("busy", "busy", true))
      .mockResolvedValueOnce({ mode: "terminal", ref: "test-ref" });
    const interruptSpy = vi.spyOn(client, "interrupt");
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Terminal"));

    await waitFor(() =>
      expect(screen.getByText("Can't switch — a turn is in flight.")).toBeTruthy(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Interrupt and switch" }));

    await waitFor(() => expect(interruptSpy).toHaveBeenCalledWith("session-active"));
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
  });

  it("shows a resume-failed failure with a retry that succeeds", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "switchTransport")
      .mockRejectedValueOnce(new TransportSwitchError("resume failed", "resume_failed", true))
      .mockResolvedValueOnce({ mode: "terminal", ref: "test-ref" });
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Terminal"));

    await waitFor(() => expect(screen.getByText("Couldn't switch transport.")).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
  });

  it("shows the summary card for an inactive session with a summary", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-inactive" routerClient={client} />);

    await waitFor(() => expect(screen.getByText("Accomplished")).toBeTruthy());
  });

  it("toggles the Shell dock from the header kebab menu", async () => {
    const client = new FakeRouterClient();
    const bridge = makeStubBridge();
    render(
      <ConversationTab sessionId="session-active" routerClient={client} ptyBridge={bridge} />,
    );

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    expect(document.querySelector('[data-testid="resize-handle"]')).toBeNull();

    fireEvent.click(screen.getByLabelText("More actions"));
    fireEvent.click(screen.getByText("Shell"));

    await waitFor(() =>
      expect(document.querySelector('[data-testid="resize-handle"]')).toBeTruthy(),
    );
  });

  it("shows an error card when getSession rejects, and Retry refetches", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockRejectedValueOnce(new Error("boom"));
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText(/couldn't load/i)).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
  });

  it("shows an error card when getTranscript rejects", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockRejectedValueOnce(new Error("boom"));
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText(/couldn't load/i)).toBeTruthy());
  });

  it("shows a loading placeholder before the fetches resolve", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    expect(screen.getByText(/loading session/i)).toBeTruthy();

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
  });

  it("shows an empty-state note when the transcript is empty, with the composer still usable", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText("No conversation yet")).toBeTruthy());
    expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy();
  });
});
