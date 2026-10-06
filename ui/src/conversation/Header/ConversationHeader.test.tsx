import { describe, expect, it, vi, afterEach, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConversationHeader } from "./ConversationHeader";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import { agentIndex } from "../../router/agentRef";
import { resetProviderModelCache } from "../../router/useProviderModels";
import { resetProviderInfoCache } from "../../router/useProviderInfos";

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

function subline(): string | null {
  return screen.queryByTestId("conversation-subline")?.textContent?.replace(/\s+/g, " ").trim() ?? null;
}

describe("ConversationHeader", () => {
  // useProviderModels dedupes concurrent mounts behind a module-level
  // in-flight request; without this a case can join the PREVIOUS case's
  // still-unresolved fetch and see its stubbed models.
  beforeEach(() => {
    resetProviderModelCache();
    resetProviderInfoCache();
  });

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

  it("renders the location / branch line but not a duplicate name or status chip", () => {
    render(<ConversationHeader session={fixtureSessions["session-active"]} workspaceName="mads" />);

    // The name lives only in the SessionWorkspace bar above; the activity
    // chip is gone altogether.
    expect(screen.queryByText("Add retry backoff to the ingest worker")).toBeNull();
    expect(screen.queryByText(/^active$/i)).toBeNull();
    expect(subline()).toBe("mads / mads/tsk_retry-backoff");
  });

  // session.workspace is an id-or-name reference the caller resolves; the
  // subline prints the resolved name or the folder, never `wsp_...`.
  it("never prints the session's raw workspace reference", () => {
    render(
      <ConversationHeader
        session={{ ...fixtureSessions["session-active"], workspace: "wsp_b8439a998b38a07f" }}
      />,
    );

    expect(screen.queryByText(/wsp_/)).toBeNull();
    expect(subline()).toBe("mads/tsk_retry-backoff");
  });

  it("names an isolated session by its workspace, then branch → base, with both paths on hover", () => {
    render(<ConversationHeader session={fixtureIsolatedSession} workspaceName="mads" />);

    expect(subline()).toBe("mads / mads/session/session-isolated → main");
    expect(screen.getByTestId("conversation-subline").getAttribute("title")).toBe(
      "Folder: /home/user/repo/mads\nWorktree: /home/user/.mads/session-worktrees/session-isolated",
    );
  });

  // Same rule as the rail card: no workspace → the chosen folder's basename,
  // and for an isolated session never the worktree's own (session-id) name.
  it("falls back to the folder name when there is no workspace", () => {
    render(<ConversationHeader session={{ ...fixtureIsolatedSession, workspace: undefined }} />);
    expect(subline()).toBe("mads / mads/session/session-isolated → main");
  });

  it("shows the checkout's branch for a session working in place", () => {
    const inPlace = {
      ...fixtureSessions["session-active"],
      workspace: undefined,
      worktree_branch: undefined,
      dir: "/home/user/scratch/notes",
    };
    render(<ConversationHeader session={inPlace} checkoutBranch="main" />);
    expect(subline()).toBe("notes / main");
    expect(screen.getByTestId("conversation-subline").getAttribute("title")).toBe(
      "Folder: /home/user/scratch/notes",
    );
  });

  it("shows just the location when no branch is known", () => {
    const inPlace = {
      ...fixtureSessions["session-active"],
      worktree_branch: undefined,
      dir: "/home/user/scratch/notes",
    };
    render(<ConversationHeader session={inPlace} workspaceName="mads" />);
    expect(subline()).toBe("mads");
  });

  describe("persona pill", () => {
    const byId = { ...fixtureSessions["session-active"], agent: "agt_3212544bb2e37c92" };
    const partner = {
      id: "agt_3212544bb2e37c92",
      name: "mads-coding-partner",
      prompt: "",
      created_at: "",
      updated_at: "",
    };

    it("names the persona an agt_ reference points at", () => {
      render(<ConversationHeader session={byId} agents={agentIndex([partner])} />);
      expect(screen.getByRole("button", { name: "mads-coding-partner" })).toBeTruthy();
      expect(screen.queryByText(/agt_/i)).toBeNull();
    });

    it("reads an unresolvable id as Unknown agent, never the id", () => {
      render(<ConversationHeader session={byId} agents={agentIndex([])} />);
      expect(screen.getByRole("button", { name: "Unknown agent" })).toBeTruthy();
      expect(screen.queryByText(/agt_/i)).toBeNull();
    });

    it("shows a neutral label, not the id, while personas are loading", () => {
      render(<ConversationHeader session={byId} />);
      expect(screen.getByRole("button", { name: "Agent" })).toBeTruthy();
      expect(screen.queryByText(/agt_/i)).toBeNull();
    });

    it("passes a name reference through", () => {
      render(<ConversationHeader session={fixtureSessions["session-active"]} agents={agentIndex([])} />);
      expect(screen.getByRole("button", { name: "autonomous-executor" })).toBeTruthy();
    });
  });

  // onChangeModel, not onUpdateSession: a model switch persists and
  // reconfigures the running agent (POST /sessions/{id}/model), so it has its
  // own prop like onChangeTrust does. It used to ride onUpdateSession, which
  // ConversationTab handled as local state only — the pill relabelled and
  // nothing else happened.
  it("switches models from the provider dropdown via onChangeModel", async () => {
    stubProviderModels([
      { id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true },
      { id: "claude-opus-5", name: "Claude Opus 5" },
    ]);
    const onChangeModel = vi.fn();
    const onUpdateSession = vi.fn();
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        onChangeModel={onChangeModel}
        onUpdateSession={onUpdateSession}
      />,
    );

    // Discovery must land so the pill is a dropdown, not the static fallback chip.
    await waitFor(() => expect(screen.getByText("Claude Sonnet 5")).toBeTruthy());
    fireEvent.click(screen.getByText("Claude Sonnet 5"));
    fireEvent.click(screen.getByText("Claude Opus 5"));

    expect(onChangeModel).toHaveBeenCalledWith("claude-opus-5");
    expect(onUpdateSession).not.toHaveBeenCalled();
  });

  it("shows the session's trust level, defaulting the display to limited when unset", () => {
    // fixtureSessions["session-active"] has no trust — the backend's builtin
    // default is limited, so the pill must not claim anything stronger.
    render(<ConversationHeader session={fixtureSessions["session-active"]} />);
    expect(screen.getByText("Trust: Limited")).toBeTruthy();

    render(
      <ConversationHeader session={{ ...fixtureSessions["session-active"], trust: "full" }} />,
    );
    expect(screen.getByText("Trust: Full access")).toBeTruthy();
  });

  it("changes trust from the dropdown via onChangeTrust", () => {
    const onChangeTrust = vi.fn();
    render(
      <ConversationHeader
        session={{ ...fixtureSessions["session-active"], trust: "limited" }}
        onChangeTrust={onChangeTrust}
      />,
    );

    fireEvent.click(screen.getByText("Trust: Limited"));
    fireEvent.click(screen.getByRole("option", { name: "Full access" }));

    expect(onChangeTrust).toHaveBeenCalledWith("full");
  });

  it("shows a trust fallback on the pill, with the reason on hover", () => {
    render(
      <ConversationHeader
        session={{
          ...fixtureSessions["session-active"],
          provider: "kiro-cli",
          trust: "auto_review",
          effective_trust: "limited",
        }}
        onChangeTrust={() => {}}
      />,
    );

    const pill = screen.getByText("Trust: Auto-review → Limited");
    expect(pill.closest("button")?.getAttribute("title")).toBe(
      "kiro-cli doesn't support auto-review; running as Limited",
    );
  });

  it("shows no fallback when the session runs at the level it asked for", () => {
    render(
      <ConversationHeader
        session={{ ...fixtureSessions["session-active"], provider: "claude-code", trust: "auto_review" }}
      />,
    );

    const chip = screen.getByText("Trust: Auto-review");
    expect(chip.getAttribute("title")).toBeNull();
  });

  it("marks Auto-review in the menu when the session's provider has no reviewer", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.endsWith("/providers")) {
          return Promise.resolve(
            new Response(
              JSON.stringify([
                { name: "kiro-cli", matrix: { trust_limited: "emulated", trust_auto_review: "unsupported" } },
              ]),
              { status: 200 },
            ),
          );
        }
        return Promise.resolve(new Response(JSON.stringify({ models: [] }), { status: 200 }));
      }),
    );
    const onChangeTrust = vi.fn();
    render(
      <ConversationHeader
        session={{ ...fixtureSessions["session-active"], provider: "kiro-cli", trust: "limited" }}
        onChangeTrust={onChangeTrust}
      />,
    );

    fireEvent.click(screen.getByText("Trust: Limited"));
    const option = await screen.findByRole("option", {
      name: "Auto-review (runs as Limited on kiro-cli)",
    });
    fireEvent.click(option);
    expect(onChangeTrust).toHaveBeenCalledWith("auto_review");
  });

  it("renders trust as a static chip when no onChangeTrust handler is wired", () => {
    render(<ConversationHeader session={{ ...fixtureSessions["session-active"], trust: "full" }} />);

    const chip = screen.getByText("Trust: Full access");
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

  // The meter moved to the composer, beside Send.
  it("no longer carries the context meter", () => {
    render(<ConversationHeader session={fixtureSessions["session-active"]} />);
    expect(screen.queryByTestId("context-meter")).toBeNull();
  });
});
