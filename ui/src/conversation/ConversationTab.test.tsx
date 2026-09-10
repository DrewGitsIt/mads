// @vitest-environment jsdom
// ChatRenderer/SessionSummaryCard render markdown through DOMPurify, which
// doesn't sanitize correctly under happy-dom (see ChatRenderer/markdown.test.tsx).
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { requestStreamReattach } from "./streamReattachSignal";
import { TransportSwitchError, SessionWakeError } from "../router/client";
import { FLAGS } from "../featureFlags";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import type { Link } from "../router/types";

// Same fake-Monaco precedent as Composer.test.tsx/TerminalComposer.test.tsx —
// drives the real onMount callback (addCommand for ⌘Enter send) so the
// terminal-mode compose overlay's send path is exercised the same way the
// real editor's keybinding service would.
const CTRL_CMD = 1 << 11;
const KEY_ENTER = 3;

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: {
    value: string;
    language?: string;
    onChange?: (value: string) => void;
    onMount?: (editor: unknown, monaco: unknown) => void;
  }) => {
    const commands = new Map<number, () => void>();
    const fakeEditor = {
      addCommand: (keybinding: number, handler: () => void) => {
        commands.set(keybinding, handler);
      },
      focus: () => {},
    };
    const monaco = { KeyMod: { CtrlCmd: CTRL_CMD }, KeyCode: { Enter: KEY_ENTER } };
    props.onMount?.(fakeEditor, monaco);
    return (
      <textarea
        data-testid="fake-monaco-editor"
        data-language={props.language}
        value={props.value}
        onChange={(e) => props.onChange?.(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
            commands.get(CTRL_CMD | KEY_ENTER)?.();
          }
        }}
      />
    );
  },
}));

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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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

  it("terminal mode: ⌘E opens the compose overlay, and Send delivers the exact multi-line text to the PTY via bracketed paste", async () => {
    const client = new FakeRouterClient();
    const terminalWrite = vi.spyOn(client, "terminalWrite");
    await client.switchTransport("session-active", "terminal");

    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );

    const xtermInput = document.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    xtermInput.focus();
    fireEvent.keyDown(xtermInput, { key: "e", metaKey: true });

    const editor = await screen.findByTestId("fake-monaco-editor");
    fireEvent.change(editor, { target: { value: "line one\nline two" } });
    // No stray keystrokes should have reached the pty while the overlay was open.
    expect(terminalWrite).not.toHaveBeenCalled();

    fireEvent.keyDown(editor, { key: "Enter", metaKey: true });

    await waitFor(() =>
      expect(terminalWrite).toHaveBeenCalledWith(
        "session-active",
        "\x1b[200~line one\nline two\x1b[201~",
      ),
    );
    expect(screen.queryByTestId("terminal-composer")).toBeNull();
    expect(document.activeElement).toBe(xtermInput);
  });

  it("opens a dormant session with no transport history in chat", async () => {
    const client = new FakeRouterClient();

    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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

  it("toggles the Shell dock via the cross-tree toggle signal", async () => {
    const client = new FakeRouterClient();
    const bridge = makeStubBridge();
    render(
      <ConversationTab sessionId="session-active" routerClient={client} ptyBridge={bridge} />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
    );
    expect(document.querySelector('[data-testid="resize-handle"]')).toBeNull();

    act(() => {
      window.dispatchEvent(new Event("mads:toggle-shell"));
    });

    await waitFor(() =>
      expect(document.querySelector('[data-testid="resize-handle"]')).toBeTruthy(),
    );
  });

  it("spawns the shell dock in the session's worktree_dir when set", async () => {
    const client = new FakeRouterClient();
    const bridge = makeStubBridge();
    render(
      <ConversationTab
        sessionId="session-files-clean"
        routerClient={client}
        ptyBridge={bridge}
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
    );
    act(() => {
      window.dispatchEvent(new Event("mads:toggle-shell"));
    });

    await waitFor(() =>
      expect(bridge.spawn).toHaveBeenCalledWith(
        expect.objectContaining({
          cwd: "/home/drew/.mads/worktrees/ses_x/tsk_retry-backoff",
        }),
      ),
    );
  });

  it("spawns the shell dock in the session's resolved cwd — the agent's context, not the workspace name", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValueOnce({
      id: "session-cwd",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "cwd session",
      activity: "active",
      workspace: "mads",
      cwd: "/home/drew/repo/mads",
    });
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    const bridge = makeStubBridge();
    render(
      <ConversationTab sessionId="session-cwd" routerClient={client} ptyBridge={bridge} />,
    );

    await waitFor(() => expect(screen.getByText("No conversation yet")).toBeTruthy());
    act(() => {
      window.dispatchEvent(new Event("mads:toggle-shell"));
    });

    // The agent's real cwd — never the workspace *name* ("mads") as if it were a path.
    await waitFor(() =>
      expect(bridge.spawn).toHaveBeenCalledWith(
        expect.objectContaining({ cwd: "/home/drew/repo/mads" }),
      ),
    );
  });

  it("spawns with no cwd (host picks the default) when the session exposes neither cwd nor worktree_dir", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValueOnce({
      id: "session-no-workspace",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "No workspace session",
      activity: "active",
    });
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    const bridge = makeStubBridge();
    render(
      <ConversationTab
        sessionId="session-no-workspace"
        routerClient={client}
        ptyBridge={bridge}
      />,
    );

    await waitFor(() => expect(screen.getByText("No conversation yet")).toBeTruthy());
    act(() => {
      window.dispatchEvent(new Event("mads:toggle-shell"));
    });

    await waitFor(() => expect(bridge.spawn).toHaveBeenCalled());
    expect(vi.mocked(bridge.spawn).mock.calls[0][0]?.cwd).toBeUndefined();
  });

  it("shows an error card when getSession rejects, and Retry refetches", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockRejectedValueOnce(new Error("boom"));
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText(/couldn't load/i)).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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

  it("counts cached tokens in the context meter, not just the uncached delta — and excludes output_tokens", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({
        kind: "turn_end",
        usage: {
          input_tokens: 2_000,
          output_tokens: 3_000,
          cache_read_input_tokens: 90_000,
          cache_creation_input_tokens: 5_000,
        },
      });
    });

    // Occupancy is the prompt side only: 2k input + 90k cache_read + 5k
    // cache_creation = 97k / 200k = 49% — output_tokens (3k) never
    // contributes, since it's not part of what the next turn's request
    // carries as context (regression test: previously summed input+output+
    // cache, which would have read 50%).
    await waitFor(() => expect(screen.getByText("49%")).toBeTruthy());
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

  it("keeps an errored tool_result on its card without raising the permission banner", async () => {
    // Regression: claude reports every ordinary tool failure (non-zero exit,
    // missing file) as tool_result is_error — that used to raise a
    // "blocked awaiting permission" banner on full-trust sessions and stick
    // until turn_end, reading as a silent stall. An error is card-local; only
    // a real permission_request may block the turn indicator.
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "toolu_1", name: "Bash" } });
    });
    expect(screen.getByRole("status", { name: "Using Bash…" })).toBeTruthy();

    act(() => {
      source.emit({
        kind: "tool_result",
        tool: { id: "toolu_1", name: "" },
        text: "Exit code 1",
        is_error: true,
      });
    });

    expect(screen.queryByText(/blocked awaiting permission/)).toBeNull();
    // The turn goes on: the next tool's spinner shows as usual.
    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "toolu_2", name: "Read" } });
    });
    expect(screen.getByRole("status", { name: "Using Read…" })).toBeTruthy();
  });

  it("raises the blocked banner from a pending permission_request and clears it on resolution", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "toolu_1", name: "Bash" } });
    });
    act(() => {
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-1", tool: "Bash", input: { command: "rm -rf" }, status: "pending" },
      });
    });

    // The banner names the parked tool and replaces the plain spinner.
    expect(
      screen.getByText(
        "blocked awaiting permission for Bash — allow or deny below, or raise trust in the header",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("status", { name: "Using Bash…" })).toBeNull();

    // The terminal-status broadcast (runner's finishPermission) upserts over
    // the pending entry — the banner derives from it and clears itself.
    act(() => {
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-1", tool: "Bash", input: { command: "rm -rf" }, status: "allowed" },
      });
    });
    expect(screen.queryByText(/blocked awaiting permission/)).toBeNull();
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
      expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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

  // Permission cards (chat-transport Allow/Deny for TrustLimited/ReadOnly
  // sessions — services/session/internal/runner/permissions.go). Two
  // sources feed the same card state: the live "permission_request" stream
  // event, and GET /permission-requests on mount (so a still-open card
  // survives a transcript refetch instead of being lost — acceptance
  // criterion 3).
  it("renders an Allow/Deny card from a live permission_request stream event, and resolving it calls resolvePermissionRequest", async () => {
    const client = new FakeRouterClient();
    const resolveSpy = vi.spyOn(client, "resolvePermissionRequest");
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({
        kind: "permission_request",
        permission: { id: "session-active-perm-1", tool: "Bash", input: { command: "rm -rf /tmp/x" }, status: "pending" },
      });
    });

    expect(screen.getByText("Bash")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Allow" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Allow" }));
    expect(resolveSpy).toHaveBeenCalledWith("session-active", "session-active-perm-1", "allow");
  });

  // Native compaction (Runner's resume-above-threshold trigger,
  // services/session/internal/runner/compact.go), surfaced as a visible
  // status item since headless -p mode has no interactive "resume from
  // summary" prompt.
  it("surfaces a live compact stream event as a status item, through to a completion state", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "compact", compact: { phase: "start" } });
    });
    expect(screen.getByText("Compacting conversation…")).toBeTruthy();

    act(() => {
      source.emit({ kind: "compact", compact: { phase: "done", success: true } });
    });
    expect(screen.getByText("Conversation compacted.")).toBeTruthy();
    expect(screen.queryByText("Compacting conversation…")).toBeNull();
  });

  // The context meter's data source at wake time — before this mount has
  // seen a turn_end of its own — is the session record's own last_usage
  // (services/session/internal/runner's persistUsage).
  it("seeds the context meter from session.last_usage on mount, before any turn_end", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValueOnce({
      id: "session-active",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "resumed session",
      activity: "active",
      model: "claude-sonnet-5",
      last_usage: { input_tokens: 100_000, output_tokens: 500 },
    });
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText("50%")).toBeTruthy());
  });

  // The user-facing half of auto-compaction's fix: the server only force-
  // compacts near the window limit; below that the UI offers the choice
  // (the resume options interactive claude-code shows that headless -p
  // lacks), derived locally from the same last_usage the meter reads.
  it("suggests compaction at high occupancy, and Compact now runs it", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValueOnce({
      id: "session-active",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "big session",
      activity: "active",
      model: "claude-sonnet-5",
      last_usage: { input_tokens: 500, output_tokens: 50, context_tokens: 130_000, context_window: 200_000 },
    });
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    const compactSpy = vi.spyOn(client, "compactSession").mockResolvedValue();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText(/using ~65% of the context window/)).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Compact now" }));
    expect(compactSpy).toHaveBeenCalledWith("session-active");
    // The suggestion yields to the (optimistic) progress banner immediately.
    await waitFor(() => expect(screen.getByText("Compacting conversation…")).toBeTruthy());
    expect(screen.queryByText(/of the context window/)).toBeNull();
  });

  it("Not now dismisses the compaction suggestion, and low occupancy never shows it", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValueOnce({
      id: "session-active",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "big session",
      activity: "active",
      model: "claude-sonnet-5",
      last_usage: { input_tokens: 500, output_tokens: 50, context_tokens: 130_000, context_window: 200_000 },
    });
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByText(/of the context window/)).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(screen.queryByText(/of the context window/)).toBeNull();

    // Below the 60% suggestion line (and with the accurate context_tokens
    // beating the inflated aggregate): no banner at all.
    const quiet = new FakeRouterClient();
    vi.spyOn(quiet, "getSession").mockResolvedValueOnce({
      id: "session-active",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "small session",
      activity: "active",
      model: "claude-sonnet-5",
      last_usage: { input_tokens: 500, output_tokens: 50, cache_read_input_tokens: 400_000, context_tokens: 40_000, context_window: 200_000 },
    });
    render(<ConversationTab sessionId="session-active" routerClient={quiet} />);
    // The meter proves the same accurate numbers rendered: 40k/200k = 20%,
    // not the aggregate's 400k.
    await waitFor(() => expect(screen.getByText("20%")).toBeTruthy());
    expect(screen.queryByText(/of the context window/)).toBeNull();
  });

  // A last_usage persisted before context_tokens existed carries only the
  // per-turn billing aggregate (~N× real occupancy) — off it the banner used
  // to claim an impossible "~134%" while the meter showed 100%. Untrusted
  // numbers must never raise the suggestion; the meter's clamped fallback
  // rendering is all such a session gets.
  it("never suggests compaction from an aggregate-only last_usage", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValueOnce({
      id: "session-active",
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
      name: "stale aggregate session",
      activity: "active",
      model: "claude-sonnet-5",
      last_usage: {
        input_tokens: 18,
        output_tokens: 50,
        cache_creation_input_tokens: 23_710,
        cache_read_input_tokens: 243_484,
      },
    });
    vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    // The meter's intentional fallback rendering (267,212 aggregate against
    // the static 200k table, clamped) proves the seeded usage mounted.
    await waitFor(() => expect(screen.getByText("100%")).toBeTruthy());
    expect(screen.queryByText(/of the context window/)).toBeNull();
  });

  // The "compaction completed banner sticks around far longer than it
  // should" fix: success is a transient confirmation, auto-dismissed.
  it("auto-dismisses a successful compaction banner after a few seconds", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    const source = client.sessionEventSources.at(-1)!;

    // Fake timers only from here — the mount above needs real ones for its
    // async fetches; the dismissal timer is what's under test.
    vi.useFakeTimers();
    try {
      act(() => {
        source.emit({ kind: "compact", compact: { phase: "done", success: true } });
      });
      expect(screen.getByText("Conversation compacted.")).toBeTruthy();

      act(() => {
        vi.advanceTimersByTime(6500);
      });
      expect(screen.queryByText("Conversation compacted.")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the working indicator visible for the entire live turn, until turn_end", async () => {
    const client = new FakeRouterClient();
    const getTranscript = vi
      .spyOn(client, "getTranscript")
      .mockResolvedValue([{ role: "user", text: "hi" }]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByText("hi")).toBeTruthy());
    const source = client.sessionEventSources.at(-1)!;

    // Regression: `working` used to compute false whenever the tail was a
    // finished text run or a completed tool call, blinking the indicator
    // out mid-turn — exactly when "is it done?" matters.
    act(() => source.emit({ kind: "text", text: "checking the logs" }));
    expect(screen.getByText("checking the logs")).toBeTruthy();
    expect(screen.getByLabelText("Working…")).toBeTruthy();

    act(() => source.emit({ kind: "tool_use", tool: { id: "wt1", name: "Read", path: "a.go" } }));
    expect(screen.getByLabelText("Using Read…")).toBeTruthy();

    act(() => source.emit({ kind: "tool_result", tool: { id: "wt1", name: "" }, text: "contents" }));
    expect(screen.getByLabelText("Working…")).toBeTruthy();

    getTranscript.mockResolvedValue([
      { role: "user", text: "hi" },
      { role: "agent", text: "checking the logs" },
    ]);
    await act(async () => {
      source.emit({ kind: "turn_end" });
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.queryByLabelText("Working…")).toBeNull());
  });

  it("reconstructs a still-pending permission card from GET /permission-requests on mount, surviving a transcript refetch", async () => {
    const client = new FakeRouterClient();
    client.seedPermissionRequest("session-active", {
      id: "session-active-perm-1",
      tool: "Write",
      input: { file_path: "docs/notes.md" },
      status: "pending",
    });
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText("Write")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Allow" })).toBeTruthy();
  });

  // Live-turn transcript polling (structure-as-it-streams, survive stream
  // drops) — the provider's transcript file is written continuously during
  // a turn, same premise the pipeline drill-in's turns feed already relies
  // on, so periodically re-fetching it mid-turn is what kills the
  // concatenated-blob bug: completed paragraphs/tool cards show up without
  // waiting for turn_end.
  describe("live-turn streaming", () => {
    it("surfaces separate paragraphs and tool cards live before turn_end, with no mid-turn transcript poll", async () => {
      const client = new FakeRouterClient();
      const getTranscript = vi
        .spyOn(client, "getTranscript")
        .mockResolvedValue([{ role: "user", text: "investigate the bug" }]);
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(screen.getByText("investigate the bug")).toBeTruthy());
      const source = client.sessionEventSources.at(-1)!;
      const callsAfterLoad = getTranscript.mock.calls.length;

      // Built entirely from the live stream: text, then a tool, then more text.
      act(() => {
        source.emit({ kind: "text", text: "first, let me check the logs" });
        source.emit({ kind: "tool_use", tool: { id: "toolu_1", name: "Read", path: "a.go" } });
        source.emit({ kind: "text", text: "found it — a missing nil check" });
      });

      // Structure appears BEFORE turn_end — two distinct paragraphs split by
      // a live activity group (open, growing in place), straight off the
      // stream.
      expect(screen.getByText("first, let me check the logs")).toBeTruthy();
      expect(screen.getByText("found it — a missing nil check")).toBeTruthy();
      expect(screen.getByTestId("activity-group")).toBeTruthy();
      // The live group is open (its row shows) and the working indicator's
      // ticker names the same current activity.
      expect(screen.getAllByText("a.go").length).toBeGreaterThanOrEqual(1);
      expect(document.querySelectorAll('[class*="prose"]').length).toBeGreaterThanOrEqual(3);
      // The rubber-band source is gone: no transcript is polled mid-turn.
      expect(getTranscript.mock.calls.length).toBe(callsAfterLoad);
    });

    it("appends text tokens smoothly into one paragraph without resetting", async () => {
      const client = new FakeRouterClient();
      vi.spyOn(client, "getTranscript").mockResolvedValue([{ role: "user", text: "hi" }]);
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(screen.getByText("hi")).toBeTruthy());
      const source = client.sessionEventSources.at(-1)!;

      // Token-by-token deltas accumulate into a single growing run — never
      // cleared between chunks.
      act(() => source.emit({ kind: "text", text: "Hel" }));
      act(() => source.emit({ kind: "text", text: "lo, wor" }));
      act(() => source.emit({ kind: "text", text: "ld" }));

      expect(screen.getByText("Hello, world")).toBeTruthy();
    });

    it("recovers dropped frames by refetching the transcript on reconnect", async () => {
      const client = new FakeRouterClient();
      const getTranscript = vi
        .spyOn(client, "getTranscript")
        .mockResolvedValueOnce([{ role: "user", text: "ping" }])
        .mockResolvedValue([
          { role: "user", text: "ping" },
          { role: "agent", text: "done — here is the fix" },
        ]);
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(screen.getByText("ping")).toBeTruthy());
      const source = client.sessionEventSources.at(-1)!;

      // A turn is live (text streamed), then the stream drops — later frames
      // are lost, standing in for a throttled/dropped EventSource.
      act(() => source.emit({ kind: "text", text: "partial before the drop" }));
      expect(screen.getByText("partial before the drop")).toBeTruthy();
      const callsBefore = getTranscript.mock.calls.length;

      // Reconnect: the browser's EventSource fires "open"; a live turn triggers
      // an immediate transcript refetch to recover what the drop missed.
      await act(async () => {
        source.emitOpen();
        await Promise.resolve();
      });
      expect(getTranscript.mock.calls.length).toBe(callsBefore + 1);
      expect(screen.getByText("done — here is the fix")).toBeTruthy();
    });

    it("force-reattaches the stream on a mads:reattach-stream signal for this session, even while the cached record says inactive", async () => {
      const client = new FakeRouterClient({
        ...fixtureSessions,
        "session-active": { ...fixtureSessions["session-active"], activity: "inactive" },
      });
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() => expect(client.sessionEventSources.length).toBe(1));

      // A signal for a different session must be ignored.
      act(() => requestStreamReattach("some-other-session"));
      expect(client.sessionEventSources.length).toBe(1);

      act(() => requestStreamReattach("session-active"));
      await waitFor(() => expect(client.sessionEventSources.length).toBe(2));
    });

    it("never polls the transcript while idle", async () => {
      const client = new FakeRouterClient();
      const getTranscript = vi.spyOn(client, "getTranscript");
      render(<ConversationTab sessionId="session-active" routerClient={client} />);

      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Chat" })).toBeTruthy(),
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
