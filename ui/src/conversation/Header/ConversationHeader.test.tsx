import { describe, expect, it, vi, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConversationHeader } from "./ConversationHeader";
import { fixtureSessions } from "../../router/fixtures";

function stubProviderModels(models: { id: string; name: string; default?: boolean }[]) {
  const fetchMock = vi.fn().mockImplementation((input: URL) => {
    const url = input.toString();
    if (url.endsWith("/providers")) {
      return Promise.resolve(new Response(JSON.stringify([{ name: "claudecode", matrix: {} }]), { status: 200 }));
    }
    return Promise.resolve(new Response(JSON.stringify({ models }), { status: 200 }));
  });
  vi.stubGlobal("fetch", fetchMock);
}

describe("ConversationHeader", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the resolved model display name when discovery finds a match", async () => {
    stubProviderModels([{ id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true }]);

    render(<ConversationHeader session={fixtureSessions["session-active"]} />);

    await waitFor(() => expect(screen.getByText("Claude Sonnet 5")).toBeTruthy());
  });

  it("falls back to the raw model ID when discovery has no match", async () => {
    stubProviderModels([{ id: "claude-opus-4-8", name: "Claude Opus 4.8" }]);

    render(<ConversationHeader session={fixtureSessions["session-active"]} />);

    await waitFor(() => expect(screen.getByText("claude-sonnet-5")).toBeTruthy());
  });

  it("renders the workspace/branch line but not a duplicate name or status chip", () => {
    render(<ConversationHeader session={fixtureSessions["session-active"]} />);

    // Name + activity now live only in the SessionWorkspace bar above.
    expect(screen.queryByText("Add retry backoff to the ingest worker")).toBeNull();
    expect(screen.queryByText("active")).toBeNull();
    expect(screen.getByText(/mads\/tsk_retry-backoff/)).toBeTruthy();
  });

  it("switches models from the provider dropdown via onUpdateSession", async () => {
    stubProviderModels([
      { id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true },
      { id: "claude-opus-4-8", name: "Claude Opus 4.8" },
    ]);
    const onUpdateSession = vi.fn();
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        onUpdateSession={onUpdateSession}
      />,
    );

    // Discovery must land so the pill is a dropdown, not the static fallback chip.
    await waitFor(() => expect(screen.getByText("Claude Sonnet 5")).toBeTruthy());
    fireEvent.click(screen.getByText("Claude Sonnet 5"));
    fireEvent.click(screen.getByText("Claude Opus 4.8"));

    expect(onUpdateSession).toHaveBeenCalledWith({ model: "claude-opus-4-8" });
  });

  it("no longer renders Shell / Restart Agent (moved to the workspace kebab)", () => {
    render(<ConversationHeader session={fixtureSessions["session-active"]} />);

    expect(screen.queryByLabelText("More actions")).toBeNull();
    expect(screen.queryByText("Shell")).toBeNull();
    expect(screen.queryByText("Restart Agent")).toBeNull();
  });

  it("renders the Chat/Terminal switch in the header and reports changes via onModeChange", () => {
    const onModeChange = vi.fn();
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        mode="chat"
        onModeChange={onModeChange}
      />,
    );

    const chat = screen.getByRole("button", { name: /^chat$/i });
    const terminal = screen.getByRole("button", { name: /^terminal$/i });
    expect(chat.getAttribute("aria-pressed")).toBe("true");
    expect(terminal.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(terminal);
    expect(onModeChange).toHaveBeenCalledWith("terminal");
  });

  it("omits the mode switch when no mode/onModeChange is supplied", () => {
    render(<ConversationHeader session={fixtureSessions["session-active"]} />);
    expect(screen.queryByRole("button", { name: /^terminal$/i })).toBeNull();
  });

  it("renders the context meter when usage numbers are supplied", () => {
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        usedTokens={50000}
        contextWindow={200000}
      />,
    );
    expect(screen.getByText("25%")).toBeTruthy();
  });
});
