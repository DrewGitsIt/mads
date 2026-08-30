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
    stubProviderModels([{ id: "claude-opus-5", name: "Claude Opus 5" }]);

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
      { id: "claude-opus-5", name: "Claude Opus 5" },
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
    fireEvent.click(screen.getByText("Claude Opus 5"));

    expect(onUpdateSession).toHaveBeenCalledWith({ model: "claude-opus-5" });
  });

  it("shows the session's trust level, defaulting the display to limited when unset", () => {
    // fixtureSessions["session-active"] has no trust — the backend's builtin
    // default is limited, so the pill must not claim anything stronger.
    render(<ConversationHeader session={fixtureSessions["session-active"]} />);
    expect(screen.getByText("trust: Limited")).toBeTruthy();

    render(
      <ConversationHeader session={{ ...fixtureSessions["session-active"], trust: "full" }} />,
    );
    expect(screen.getByText("trust: Full access")).toBeTruthy();
  });

  it("changes trust from the dropdown via onChangeTrust", () => {
    const onChangeTrust = vi.fn();
    render(
      <ConversationHeader
        session={{ ...fixtureSessions["session-active"], trust: "limited" }}
        onChangeTrust={onChangeTrust}
      />,
    );

    fireEvent.click(screen.getByText("trust: Limited"));
    fireEvent.click(screen.getByRole("option", { name: "Full access" }));

    expect(onChangeTrust).toHaveBeenCalledWith("full");
  });

  it("renders trust as a static chip when no onChangeTrust handler is wired", () => {
    render(<ConversationHeader session={{ ...fixtureSessions["session-active"], trust: "full" }} />);

    const chip = screen.getByText("trust: Full access");
    fireEvent.click(chip);
    expect(screen.queryByRole("listbox", { name: "Trust level" })).toBeNull();
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
        usage={{ input_tokens: 50000, output_tokens: 0 }}
        contextWindow={200000}
      />,
    );
    expect(screen.getByText("25%")).toBeTruthy();
  });
});
