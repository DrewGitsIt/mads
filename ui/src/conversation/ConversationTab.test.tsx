// @vitest-environment jsdom
// ChatRenderer/SessionSummaryCard render markdown through DOMPurify, which
// doesn't sanitize correctly under happy-dom (see ChatRenderer/markdown.test.tsx).
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
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
  it("renders the chat display mode by default", async () => {
    const client = new FakeRouterClient();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    expect(screen.getByText(/Add retry backoff to the ingest worker, please/)).toBeTruthy();
  });

  it("switches to terminal display mode and mounts TerminalView against the pty bridge", async () => {
    const client = new FakeRouterClient();
    const bridge = makeStubBridge();
    render(
      <ConversationTab sessionId="session-active" routerClient={client} ptyBridge={bridge} />,
    );

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Terminal"));

    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));
    expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy();
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
});
