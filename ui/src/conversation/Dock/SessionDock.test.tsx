import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SessionDock, type SessionDockProps } from "./SessionDock";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import { agentIndex } from "../../router/agentRef";
import { resetProviderModelCache } from "../../router/useProviderModels";
import { resetProviderInfoCache } from "../../router/useProviderInfos";
import { hotkeyLabel } from "../../platform/hotkeys";

function stubProviderModels(models: { id: string; name: string; default?: boolean }[]) {
  const fetchMock = vi.fn().mockImplementation((input: URL) => {
    const url = input.toString();
    if (url.endsWith("/providers")) {
      return Promise.resolve(new Response(JSON.stringify([{ name: "claudecode", matrix: {} }]), { status: 200 }));
    }
    return Promise.resolve(new Response(JSON.stringify({ models }), { status: 200 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderDock(props: Partial<SessionDockProps> = {}) {
  const onModeChange = vi.fn();
  const utils = render(
    <SessionDock session={fixtureSessions["session-active"]} mode="chat" onModeChange={onModeChange} {...props} />,
  );
  return { ...utils, onModeChange };
}

function location(): string | null {
  return screen.queryByTestId("dock-location")?.getAttribute("aria-label") ?? null;
}

describe("SessionDock", () => {
  // useProviderModels dedupes concurrent mounts behind a module-level
  // in-flight request; without this a case can join the PREVIOUS case's
  // still-unresolved fetch and see its stubbed models.
  beforeEach(() => {
    resetProviderModelCache();
    resetProviderInfoCache();
    stubProviderModels([]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("model segment", () => {
    it("shows the resolved model display name when discovery finds a match", async () => {
      stubProviderModels([{ id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true }]);
      renderDock();
      expect(await screen.findByRole("button", { name: "Claude Sonnet 5" })).toBeTruthy();
    });

    it("falls back to the raw model ID when discovery has no match", async () => {
      stubProviderModels([{ id: "claude-opus-5", name: "Claude Opus 5" }]);
      renderDock();
      expect(await screen.findByRole("button", { name: "claude-sonnet-5" })).toBeTruthy();
    });

    it("is a static segment, not an empty menu, when the provider lists no models", async () => {
      const fetchMock = stubProviderModels([]);
      renderDock();
      await waitFor(() => expect(fetchMock).toHaveBeenCalled());
      const seg = document.querySelector('[data-segment="model"]')!;
      expect(seg.tagName).toBe("SPAN");
      expect(seg.getAttribute("aria-label")).toBe("claude-sonnet-5");
      expect(screen.queryByRole("button", { name: "claude-sonnet-5" })).toBeNull();
    });

    // A model switch persists and reconfigures the running agent
    // (POST /sessions/{id}/model).
    it("switches models from the provider dropdown via onChangeModel", async () => {
      stubProviderModels([
        { id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true },
        { id: "claude-opus-5", name: "Claude Opus 5" },
      ]);
      const onChangeModel = vi.fn();
      renderDock({ onChangeModel });

      fireEvent.click(await screen.findByRole("button", { name: "Claude Sonnet 5" }));
      fireEvent.click(screen.getByRole("option", { name: "Claude Opus 5" }));

      expect(onChangeModel).toHaveBeenCalledWith("claude-opus-5");
      expect(screen.queryByRole("listbox", { name: "Model" })).toBeNull();
    });

    it("doesn't call onChangeModel when the current model is picked again", async () => {
      stubProviderModels([{ id: "claude-sonnet-5", name: "Claude Sonnet 5" }]);
      const onChangeModel = vi.fn();
      renderDock({ onChangeModel });
      fireEvent.click(await screen.findByRole("button", { name: "Claude Sonnet 5" }));
      fireEvent.click(screen.getByRole("option", { name: "Claude Sonnet 5" }));
      expect(onChangeModel).not.toHaveBeenCalled();
    });
  });

  describe("location segment", () => {
    it("reads workspace / branch, with no session name or status chip", () => {
      renderDock({ workspaceName: "mads" });
      expect(screen.queryByText("Add retry backoff to the ingest worker")).toBeNull();
      expect(location()).toBe("mads / mads/tsk_retry-backoff");
    });

    // session.workspace is an id-or-name reference the caller resolves.
    it("never prints the session's raw workspace reference", () => {
      renderDock({ session: { ...fixtureSessions["session-active"], workspace: "wsp_b8439a998b38a07f" } });
      expect(screen.queryByText(/wsp_/)).toBeNull();
      expect(location()).toBe("mads/tsk_retry-backoff");
    });

    it("names an isolated session by its workspace, then branch → base, with both paths on hover", () => {
      renderDock({ session: fixtureIsolatedSession, workspaceName: "mads" });
      expect(location()).toBe("mads / mads/session/session-isolated → main");
      expect(screen.getByTestId("dock-location").getAttribute("title")).toBe(
        "Folder: /home/user/repo/mads\nWorktree: /home/user/.mads/session-worktrees/session-isolated",
      );
    });

    it("falls back to the folder name when there is no workspace", () => {
      renderDock({ session: { ...fixtureIsolatedSession, workspace: undefined } });
      expect(location()).toBe("mads / mads/session/session-isolated → main");
    });

    it("shows the checkout's branch for a session working in place", () => {
      const inPlace = {
        ...fixtureSessions["session-active"],
        workspace: undefined,
        worktree_branch: undefined,
        dir: "/home/user/scratch/notes",
      };
      renderDock({ session: inPlace, checkoutBranch: "main" });
      expect(location()).toBe("notes / main");
      expect(screen.getByTestId("dock-location").getAttribute("title")).toBe("Folder: /home/user/scratch/notes");
    });

    it("shows just the location when no branch is known", () => {
      const inPlace = { ...fixtureSessions["session-active"], worktree_branch: undefined, dir: "/home/user/scratch/notes" };
      renderDock({ session: inPlace, workspaceName: "mads" });
      expect(location()).toBe("mads");
    });

    it("carries the short branch id for the narrow ladder", () => {
      renderDock({ session: fixtureIsolatedSession, workspaceName: "mads" });
      expect(screen.getByTestId("dock-location").textContent).toContain("session-isol");
    });
  });

  describe("persona segment", () => {
    const byId = { ...fixtureSessions["session-active"], agent: "agt_3212544bb2e37c92" };
    const partner = {
      id: "agt_3212544bb2e37c92",
      name: "mads-coding-partner",
      prompt: "",
      created_at: "",
      updated_at: "",
    };

    it("names the persona an agt_ reference points at", () => {
      renderDock({ session: byId, agents: agentIndex([partner]) });
      expect(screen.getByLabelText("mads-coding-partner")).toBeTruthy();
      expect(screen.queryByText(/agt_/i)).toBeNull();
    });

    it("reads an unresolvable id as Unknown agent, never the id", () => {
      renderDock({ session: byId, agents: agentIndex([]) });
      expect(screen.getByLabelText("Unknown agent")).toBeTruthy();
      expect(screen.queryByText(/agt_/i)).toBeNull();
    });

    it("shows a neutral label, not the id, while personas are loading", () => {
      renderDock({ session: byId });
      expect(screen.getByLabelText("Agent")).toBeTruthy();
      expect(screen.queryByText(/agt_/i)).toBeNull();
    });

    it("passes a name reference through", () => {
      renderDock({ agents: agentIndex([]) });
      expect(screen.getByLabelText("autonomous-executor")).toBeTruthy();
    });

    it("is read-only: no editor, nothing to commit", () => {
      renderDock({ session: byId, agents: agentIndex([partner]) });
      const seg = screen.getByLabelText("mads-coding-partner");
      expect(seg.tagName).toBe("SPAN");
      fireEvent.click(seg);
      expect(screen.queryByRole("dialog", { name: "Persona" })).toBeNull();
    });
  });

  describe("trust segment", () => {
    it("shows the session's trust level, defaulting the display to limited when unset", () => {
      const { unmount } = renderDock();
      expect(screen.getByLabelText("Trust: Limited")).toBeTruthy();
      unmount();
      renderDock({ session: { ...fixtureSessions["session-active"], trust: "full" } });
      expect(screen.getByLabelText("Trust: Full access")).toBeTruthy();
    });

    it("changes trust from the upward dropdown via onChangeTrust", () => {
      const onChangeTrust = vi.fn();
      renderDock({ session: { ...fixtureSessions["session-active"], trust: "limited" }, onChangeTrust });
      fireEvent.click(screen.getByRole("button", { name: "Trust: Limited" }));
      fireEvent.click(screen.getByRole("option", { name: "Full access" }));
      expect(onChangeTrust).toHaveBeenCalledWith("full");
      expect(screen.queryByRole("listbox", { name: "Trust level" })).toBeNull();
    });

    it("shows a trust fallback on the segment, with the reason on hover", () => {
      renderDock({
        session: {
          ...fixtureSessions["session-active"],
          provider: "kiro-cli",
          trust: "auto_review",
          effective_trust: "limited",
        },
        onChangeTrust: () => {},
      });
      const seg = screen.getByRole("button", { name: "Trust: Auto-review → Limited" });
      expect(seg.getAttribute("title")).toBe("kiro-cli doesn't support auto-review; running as Limited");
    });

    it("shows no fallback when the session runs at the level it asked for", () => {
      renderDock({
        session: { ...fixtureSessions["session-active"], provider: "claude-code", trust: "auto_review" },
      });
      const seg = screen.getByLabelText("Trust: Auto-review");
      expect(seg.getAttribute("title")).toBeNull();
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
      renderDock({
        session: { ...fixtureSessions["session-active"], provider: "kiro-cli", trust: "limited" },
        onChangeTrust,
      });
      fireEvent.click(screen.getByRole("button", { name: "Trust: Limited" }));
      const option = await screen.findByRole("option", { name: "Auto-review (runs as Limited on kiro-cli)" });
      fireEvent.click(option);
      expect(onChangeTrust).toHaveBeenCalledWith("auto_review");
    });

    it("disables the restricted levels on a provider that runs at full trust only", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockImplementation((input: URL) => {
          const url = input.toString();
          if (url.endsWith("/providers")) {
            return Promise.resolve(
              new Response(
                JSON.stringify([
                  {
                    name: "hermes",
                    matrix: { trust_limited: "unsupported", trust_read_only: "unsupported", trust_auto_review: "unsupported" },
                  },
                ]),
                { status: 200 },
              ),
            );
          }
          return Promise.resolve(new Response(JSON.stringify({ models: [] }), { status: 200 }));
        }),
      );
      const onChangeTrust = vi.fn();
      renderDock({ session: { ...fixtureSessions["session-active"], provider: "hermes", trust: "full" }, onChangeTrust });
      fireEvent.click(screen.getByRole("button", { name: "Trust: Full access" }));
      await waitFor(() => expect(screen.getByRole("option", { name: "Limited" }).getAttribute("aria-disabled")).toBe("true"));
      for (const name of ["Auto-review", "Read-only"]) {
        expect(screen.getByRole("option", { name }).getAttribute("aria-disabled")).toBe("true");
      }
      fireEvent.click(screen.getByRole("option", { name: "Limited" }));
      expect(onChangeTrust).not.toHaveBeenCalled();
      expect(screen.getByRole("option", { name: "Full access" }).getAttribute("aria-disabled")).toBeNull();
    });

    it("renders trust as a static segment when no onChangeTrust handler is wired", () => {
      renderDock({ session: { ...fixtureSessions["session-active"], trust: "full" } });
      const seg = screen.getByLabelText("Trust: Full access");
      expect(seg.tagName).toBe("SPAN");
      fireEvent.click(seg);
      expect(screen.queryByRole("listbox", { name: "Trust level" })).toBeNull();
    });
  });

  describe("views", () => {
    it("has no view switch or compose segment in chat mode (the + menu owns the switch)", () => {
      renderDock({ onComposeToggle: vi.fn() });
      expect(screen.queryByRole("button", { name: "Chat" })).toBeNull();
      expect(screen.queryByRole("button", { name: /^terminal$/i })).toBeNull();
      expect(document.querySelector('[data-segment="compose"]')).toBeNull();
    });

    it("leads the terminal status line with a Chat segment that switches back", () => {
      const { onModeChange } = renderDock({ mode: "terminal" });
      const chat = screen.getByRole("button", { name: "Chat" });
      expect(screen.getByTestId("dock-status").firstElementChild).toBe(chat);
      fireEvent.click(chat);
      expect(onModeChange).toHaveBeenCalledWith("chat");
    });

    it("offers the compose segment in terminal mode, labelled with the expandEditor hotkey", () => {
      const onComposeToggle = vi.fn();
      renderDock({ mode: "terminal", onComposeToggle });
      const compose = screen.getByRole("button", { name: "Compose a long prompt" });
      expect(compose.textContent).toContain(hotkeyLabel("expandEditor"));
      expect(compose.textContent).toContain("compose");
      fireEvent.click(compose);
      expect(onComposeToggle).toHaveBeenCalled();
    });

    it("shows the compose segment pressed while the sheet is open", () => {
      renderDock({ mode: "terminal", onComposeToggle: vi.fn(), composeOpen: true });
      expect(screen.getByRole("button", { name: "Close composer" }).getAttribute("aria-pressed")).toBe("true");
    });

    it("omits the compose segment when no toggle is wired", () => {
      renderDock({ mode: "terminal" });
      expect(document.querySelector('[data-segment="compose"]')).toBeNull();
    });

    // Ported from the retired ConversationHeader: those live in the
    // workspace kebab, and the dock must not grow them back.
    it("carries no Shell / Restart Agent controls in either view", () => {
      for (const mode of ["chat", "terminal"] as const) {
        const { unmount } = renderDock({ mode });
        expect(screen.queryByLabelText("More actions")).toBeNull();
        expect(screen.queryByText("Shell")).toBeNull();
        expect(screen.queryByText("Restart Agent")).toBeNull();
        unmount();
      }
    });
  });

  describe("slots", () => {
    it("puts children between the rim and the status line", () => {
      renderDock({
        usage: { input_tokens: 1000, output_tokens: 0, context_tokens: 1000 },
        contextWindow: 200_000,
        children: <textarea aria-label="composer" />,
      });
      const rim = screen.getByTestId("context-meter");
      const input = screen.getByLabelText("composer");
      const status = screen.getByTestId("dock-status");
      expect(rim.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      expect(input.compareDocumentPosition(status) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("renders the sheet out of the flow, above the dock", () => {
      renderDock({ mode: "terminal", sheet: <div>compose sheet</div> });
      const sheet = screen.getByTestId("dock-sheet");
      expect(sheet.textContent).toBe("compose sheet");
      expect(sheet.parentElement).toBe(screen.getByTestId("session-dock"));
    });

    it("renders no sheet container without a sheet", () => {
      renderDock({ mode: "terminal" });
      expect(screen.queryByTestId("dock-sheet")).toBeNull();
    });

    it("drops the rim's Compact link in terminal mode", () => {
      renderDock({
        mode: "terminal",
        usage: { input_tokens: 0, output_tokens: 0, context_tokens: 190_000 },
        contextWindow: 200_000,
        onCompact: vi.fn(),
      });
      expect(screen.getByTestId("context-meter")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Compact" })).toBeNull();
    });

    it("offers the rim's Compact link in chat mode", () => {
      const onCompact = vi.fn();
      renderDock({
        usage: { input_tokens: 0, output_tokens: 0, context_tokens: 190_000 },
        contextWindow: 200_000,
        onCompact,
      });
      fireEvent.click(screen.getByRole("button", { name: "Compact" }));
      expect(onCompact).toHaveBeenCalled();
    });
  });
});
