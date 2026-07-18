// @vitest-environment jsdom
// ChatRenderer/SessionSummaryCard render markdown through DOMPurify, which
// doesn't sanitize correctly under happy-dom (see ChatRenderer/markdown.test.tsx).
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { TransportSwitchError, SessionWakeError } from "../router/client";
import { FLAGS } from "../featureFlags";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import type { Link } from "../router/types";

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
    FLAGS.linkedSessions = false;
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

  it("shows a waking indicator while a message to a dormant session is in flight", async () => {
    const client = new FakeRouterClient();
    let resolveSend!: () => void;
    vi.spyOn(client, "sendMessage").mockReturnValueOnce(
      new Promise((resolve) => {
        resolveSend = () => resolve(undefined);
      }),
    );
    render(<ConversationTab sessionId="session-inactive" routerClient={client} />);

    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const textarea = screen.getByPlaceholderText(/message the agent/i);
    fireEvent.change(textarea, { target: { value: "hello" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(screen.getByText("Waking the agent…")).toBeTruthy());
    const sendButton = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    expect(sendButton.disabled).toBe(true);

    resolveSend();
    await waitFor(() => expect(screen.queryByText("Waking the agent…")).toBeNull());
  });

  it("keeps the composer text and shows the structured reason when waking a dormant session fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(
      new SessionWakeError("provider gone", "provider_missing", false),
    );
    render(<ConversationTab sessionId="session-inactive" routerClient={client} />);

    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const textarea = screen.getByPlaceholderText(/message the agent/i) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "hello" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(screen.getByText("Couldn't send your message")).toBeTruthy());
    expect(screen.getByText("provider gone")).toBeTruthy();
    expect(textarea.value).toBe("hello");
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

  it("shows the provisioning strip when provisioning, and clears it via onProvisioningDone once the session goes active", async () => {
    const client = new FakeRouterClient();
    const onProvisioningDone = vi.fn();
    render(
      <ConversationTab
        sessionId="session-active"
        routerClient={client}
        provisioning
        onProvisioningDone={onProvisioningDone}
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("status", { name: "Session provisioning" })).toBeTruthy(),
    );

    const source = FakeEventSource.instances.find((s) => s.url.includes("session.provisioning"));
    source?.emit(
      "session.activity_changed",
      JSON.stringify({
        seq: 1,
        type: "session.activity_changed",
        actor: "system",
        session_id: "session-active",
      }),
    );

    await waitFor(() => expect(onProvisioningDone).toHaveBeenCalledOnce());
  });

  it("auto-sends pendingFirstMessage through the composer and shows it as the first message", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    render(
      <ConversationTab
        sessionId="session-active"
        routerClient={client}
        pendingFirstMessage="fix the deploy script"
      />,
    );

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "fix the deploy script",
        mentions: [],
      }),
    );
    await waitFor(() => expect(screen.getByText("fix the deploy script")).toBeTruthy());
  });

  it("opens with the composer pre-filled and a toast when the auto-sent first message fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(new Error("network down"));
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    render(
      <ConversationTab
        sessionId="session-active"
        routerClient={client}
        pendingFirstMessage="fix the deploy script"
      />,
    );

    await waitFor(() => expect(screen.getByText("Couldn't send your message")).toBeTruthy());
    expect(screen.getByText("network down")).toBeTruthy();
    const textarea = screen.getByPlaceholderText(/message the agent/i) as HTMLTextAreaElement;
    expect(textarea.value).toBe("fix the deploy script");
  });

  it("calls onPendingFirstMessageHandled once on mount when pendingFirstMessage is set", async () => {
    const client = new FakeRouterClient();
    const onPendingFirstMessageHandled = vi.fn();
    render(
      <ConversationTab
        sessionId="session-active"
        routerClient={client}
        pendingFirstMessage="fix the deploy script"
        onPendingFirstMessageHandled={onPendingFirstMessageHandled}
      />,
    );

    await waitFor(() => expect(onPendingFirstMessageHandled).toHaveBeenCalledOnce());
  });

  it("with the linked-sessions flag on, fetches this session's links and renders an attributed inject in the transcript", async () => {
    FLAGS.linkedSessions = true;
    const link: Link = {
      id: "lnk_1",
      granting_session_id: "leader-session",
      subject_session_id: "session-active",
      grants: ["read", "inject", "orchestrate"],
      cadence: "per_turn",
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify([link]), { status: 200 })),
    );
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([
      {
        role: "user",
        text: "kick off the payments refactor",
        attribution: { link_id: "lnk_1", source_name: "Leader Session" },
      },
    ]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText(/⇄ from Leader · Leader Session/)).toBeTruthy());
    expect(screen.getByText("kick off the payments refactor")).toBeTruthy();
  });
});
