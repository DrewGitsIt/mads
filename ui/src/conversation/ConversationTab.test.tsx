// @vitest-environment jsdom
// ChatRenderer/SessionSummaryCard render markdown through DOMPurify, which
// doesn't sanitize correctly under happy-dom (see ChatRenderer/markdown.test.tsx).
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
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
    vi.useRealTimers();
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

  it("mounts directly into terminal mode when that's the session's live transport, without switching", async () => {
    const client = new FakeRouterClient();
    await client.switchTransport("session-active", "terminal");
    const switchSpy = vi.spyOn(client, "switchTransport");

    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
    expect(
      screen.getByRole("button", { name: "Terminal" }).getAttribute("aria-pressed"),
    ).toBe("true");
    expect(switchSpy).not.toHaveBeenCalled();
  });

  it("opens a dormant session with no transport history in chat", async () => {
    const client = new FakeRouterClient();

    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    expect(document.querySelector('[data-testid="terminal-view"]')).toBeNull();
    expect(screen.getByRole("button", { name: "Chat" }).getAttribute("aria-pressed")).toBe(
      "true",
    );
  });

  it("keeps a session's mode intact after navigating to another session and back, without an implicit switch", async () => {
    const client = new FakeRouterClient();
    const { rerender } = render(
      <ConversationTab sessionId="session-active" routerClient={client} />,
    );

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Terminal"));
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );

    const switchSpy = vi.spyOn(client, "switchTransport");

    rerender(<ConversationTab sessionId="session-inactive" routerClient={client} />);
    await waitFor(() => expect(screen.getByText("Accomplished")).toBeTruthy());
    expect(document.querySelector('[data-testid="terminal-view"]')).toBeNull();

    rerender(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
    expect(
      screen.getByRole("button", { name: "Terminal" }).getAttribute("aria-pressed"),
    ).toBe("true");

    expect(switchSpy).not.toHaveBeenCalled();
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

  // Chat replies stream live with a working indicator — the stream rides
  // the same eventSource SessionTerminalBridge uses for terminal mode;
  // FakeRouterClient.sessionEventSources tracks each instance streamSession
  // hands out so a test can grab the latest and call .emit(...) on it.

  it("renders a reply that arrives long after send, well past the old ~3s bounded-poll window", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript")
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([
        { role: "user", text: "hello" },
        { role: "agent", text: "a reply after a long gap" },
      ]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const textarea = screen.getByPlaceholderText(/message the agent/i);
    fireEvent.change(textarea, { target: { value: "hello" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByText("hello")).toBeTruthy());

    const source = client.sessionEventSources.at(-1)!;
    // Real (short) delay standing in for "long after send" — the old
    // pollForReply gave up after ~3s (10 x 300ms) of real elapsed time, so a
    // stream chunk that only arrives after the event loop has turned over
    // several times, with nothing having polled or timed out in between,
    // proves there's no bounded window standing in the way anymore.
    await new Promise((resolve) => setTimeout(resolve, 10));

    act(() => {
      source.emit({ kind: "text", text: "a reply after a long gap" });
      source.emit({ kind: "turn_end" });
    });

    expect(screen.getByText("a reply after a long gap")).toBeTruthy();
  });

  it("names the tool in the working indicator, then clears it once text starts streaming", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "tool_use", tool: { name: "Read" } });
    });
    expect(screen.getByRole("status", { name: "Using Read…" })).toBeTruthy();

    act(() => {
      source.emit({ kind: "text", text: "here's what I found" });
    });
    expect(screen.queryByRole("status", { name: "Using Read…" })).toBeNull();
  });

  it("clears the streamed bubble and replaces it with the persisted transcript on turn_end", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript")
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([{ role: "agent", text: "the persisted reply" }]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "text", text: "streaming so far" });
    });
    expect(screen.getByText("streaming so far")).toBeTruthy();

    act(() => {
      source.emit({ kind: "turn_end" });
    });

    await waitFor(() => expect(screen.getByText("the persisted reply")).toBeTruthy());
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("surfaces a stream error via the existing toast and clears the indicator", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "tool_use", tool: { name: "Read" } });
    });
    expect(screen.getByRole("status", { name: "Using Read…" })).toBeTruthy();

    act(() => {
      source.emit({ kind: "error", text: "the agent process crashed" });
    });

    await waitFor(() => expect(screen.getByText("Couldn't send your message")).toBeTruthy());
    expect(screen.getByText("the agent process crashed")).toBeTruthy();
    expect(screen.queryByRole("status", { name: "Using Read…" })).toBeNull();
  });

  it("shows the working indicator on mount mid-turn, with no send ever made by this client", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    // No composer interaction anywhere in this test — the tool_use event is
    // the first thing that's happened to this turn, same as a linked-session
    // inject arriving into a session this viewer never sent to.
    act(() => {
      source.emit({ kind: "tool_use", tool: { name: "Grep" } });
    });

    expect(screen.getByRole("status", { name: "Using Grep…" })).toBeTruthy();
  });

  it("calls getTranscript at most twice across a full send→turn_end cycle — no unbounded polling", async () => {
    const client = new FakeRouterClient();
    const getTranscript = vi
      .spyOn(client, "getTranscript")
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([
        { role: "user", text: "hello" },
        { role: "agent", text: "reply" },
      ]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const textarea = screen.getByPlaceholderText(/message the agent/i);
    fireEvent.change(textarea, { target: { value: "hello" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByText("hello")).toBeTruthy());

    const source = client.sessionEventSources.at(-1)!;
    act(() => {
      source.emit({ kind: "text", text: "reply" });
      source.emit({ kind: "turn_end" });
    });

    await waitFor(() => expect(screen.getByText("reply")).toBeTruthy());
    expect(getTranscript.mock.calls.length).toBe(2);
  });

  // Regression for the 89-GETs-in-17ms duplicate-request storm: a burst of
  // envelopes that are only relevant to usePipelineRun's shared subscription
  // (task.*/stage.*/file.changed) must not trigger a Session refetch here —
  // only SESSION_SHELL_REFETCH_EVENT_TYPES membership should.
  it("does not refetch the session for a burst of pipeline/task/stage/file events unrelated to the Session object", async () => {
    const client = new FakeRouterClient();
    const getSession = vi.spyOn(client, "getSession");
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    getSession.mockClear();

    const source = FakeEventSource.instances.find((s) => s.url.includes("task.status_changed"));
    expect(source).toBeTruthy();

    act(() => {
      for (let i = 0; i < 89; i++) {
        source!.emit(
          "task.status_changed",
          JSON.stringify({
            seq: i + 1,
            type: "task.status_changed",
            actor: "system",
            session_id: "session-active",
          }),
        );
      }
    });

    expect(getSession).not.toHaveBeenCalled();
  });

  it("still refetches the session on a session.activity_changed event", async () => {
    const client = new FakeRouterClient();
    const getSession = vi.spyOn(client, "getSession");
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    getSession.mockClear();

    const source = FakeEventSource.instances.find((s) => s.url.includes("task.status_changed"));

    act(() => {
      source!.emit(
        "session.activity_changed",
        JSON.stringify({
          seq: 1,
          type: "session.activity_changed",
          actor: "system",
          session_id: "session-active",
        }),
      );
    });

    await waitFor(() => expect(getSession).toHaveBeenCalledTimes(1));
  });

  // Live-turn transcript polling (structure-as-it-streams, survive stream
  // drops) — the provider's transcript file is written continuously during
  // a turn, same premise the pipeline drill-in's turns feed already relies
  // on, so periodically re-fetching it mid-turn is what kills the
  // concatenated-blob bug: completed paragraphs/tool cards show up without
  // waiting for turn_end.
  describe("live-turn transcript polling", () => {
    it("polls the persisted transcript during a live turn, surfacing separate paragraphs and tool cards before turn_end", async () => {
      const client = new FakeRouterClient();
      const getTranscript = vi
        .spyOn(client, "getTranscript")
        .mockResolvedValueOnce([{ role: "user", text: "investigate the bug" }])
        .mockResolvedValueOnce([
          { role: "user", text: "investigate the bug" },
          { role: "agent", text: "first, let me check the logs" },
          { role: "tool", tool: { id: "toolu_1", name: "Read", path: "a.go" } },
        ])
        .mockResolvedValueOnce([
          { role: "user", text: "investigate the bug" },
          { role: "agent", text: "first, let me check the logs" },
          { role: "tool", tool: { id: "toolu_1", name: "Read", path: "a.go" } },
          { role: "agent", text: "found it — a missing nil check" },
        ]);
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(screen.getByText("investigate the bug")).toBeTruthy());
      const source = client.sessionEventSources.at(-1)!;
      // Fake timers must be in place before the turn starts — the poll
      // effect's setInterval is scheduled at that point, and swapping to
      // fake timers afterward wouldn't retroactively convert it.
      vi.useFakeTimers();
      act(() => {
        source.emit({ kind: "tool_use", tool: { name: "Read" } });
      });

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500);
      });
      // Structure has appeared BEFORE turn_end — no concatenated blob: the
      // tool card renders, and the first agent paragraph is its own block.
      expect(screen.getByText("first, let me check the logs")).toBeTruthy();
      expect(screen.getByText("Read")).toBeTruthy();
      expect(document.querySelectorAll('[class*="prose"]').length).toBeGreaterThanOrEqual(2);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500);
      });
      expect(screen.getByText("found it — a missing nil check")).toBeTruthy();
      expect(document.querySelectorAll('[class*="prose"]').length).toBeGreaterThanOrEqual(3);
      expect(getTranscript.mock.calls.length).toBe(3);
    });

    it("converges to the full structured turn via polling even if the event stream goes dead mid-turn, and refetches immediately on reconnect", async () => {
      const client = new FakeRouterClient();
      const getTranscript = vi
        .spyOn(client, "getTranscript")
        .mockResolvedValueOnce([{ role: "user", text: "ping" }])
        .mockResolvedValueOnce([
          { role: "user", text: "ping" },
          { role: "agent", text: "still working on it" },
        ])
        .mockResolvedValueOnce([
          { role: "user", text: "ping" },
          { role: "agent", text: "still working on it" },
          { role: "agent", text: "done — here is the fix" },
        ]);
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(screen.getByText("ping")).toBeTruthy());
      const source = client.sessionEventSources.at(-1)!;
      // Fake timers must be in place before the turn starts — see the
      // comment on the previous test.
      vi.useFakeTimers();
      act(() => {
        source.emit({ kind: "tool_use", tool: { name: "Read" } });
      });

      // The stream is now dead — no more text/tool_use/turn_end frames ever
      // arrive on it, standing in for a throttled/dropped EventSource.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500);
      });
      expect(screen.getByText("still working on it")).toBeTruthy();
      expect(getTranscript.mock.calls.length).toBe(2);

      // Reconnect: the browser's own EventSource fires "open" once the
      // connection is restored — that must trigger an immediate refetch,
      // not wait for the next scheduled poll.
      await act(async () => {
        source.emitOpen();
        await Promise.resolve();
      });
      expect(getTranscript.mock.calls.length).toBe(3);
      expect(screen.getByText("done — here is the fix")).toBeTruthy();
    });

    it("never polls the transcript while idle", async () => {
      const client = new FakeRouterClient();
      const getTranscript = vi.spyOn(client, "getTranscript");
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() =>
        expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
      );
      const callsAtIdle = getTranscript.mock.calls.length;

      vi.useFakeTimers();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500 * 4);
      });
      expect(getTranscript.mock.calls.length).toBe(callsAtIdle);
    });

    it("refetches transcript and session when the tab becomes visible again during a live turn", async () => {
      const client = new FakeRouterClient();
      const getSession = vi.spyOn(client, "getSession");
      const getTranscript = vi
        .spyOn(client, "getTranscript")
        .mockResolvedValueOnce([{ role: "user", text: "ping" }])
        .mockResolvedValueOnce([
          { role: "user", text: "ping" },
          { role: "agent", text: "the reply that arrived while backgrounded" },
        ]);
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(screen.getByText("ping")).toBeTruthy());
      const source = client.sessionEventSources.at(-1)!;
      act(() => {
        source.emit({ kind: "tool_use", tool: { name: "Read" } });
      });
      getSession.mockClear();
      const callsBeforeVisible = getTranscript.mock.calls.length;

      Object.defineProperty(document, "visibilityState", {
        value: "hidden",
        configurable: true,
      });
      act(() => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      // Hidden: no refetch fires.
      expect(getTranscript.mock.calls.length).toBe(callsBeforeVisible);

      Object.defineProperty(document, "visibilityState", {
        value: "visible",
        configurable: true,
      });
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
        await Promise.resolve();
      });

      expect(getTranscript.mock.calls.length).toBeGreaterThan(callsBeforeVisible);
      expect(getSession).toHaveBeenCalled();
      expect(screen.getByText("the reply that arrived while backgrounded")).toBeTruthy();
    });
  });
});
