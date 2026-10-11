import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionsSection, deriveStartIn } from "./SessionsSection";
import { fixtureConfig } from "../../../router/fixtures";
import type { Config } from "../../../router/types";
import { dirtyFields } from "../dirtyFields";
import type { UseConfig } from "../state/useConfig";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    saveFields: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function stubFetch(providerCompleter = true) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/workspaces")) {
        return Promise.resolve(
          new Response(JSON.stringify([{ id: "ws-1", name: "core", defaults: {} }]), { status: 200 }),
        );
      }
      if (url.includes("/fast-model/status")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({ mode: "provider", provider_name: "claude-code", provider_completer: providerCompleter }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("[]", { status: 200 }));
    }),
  );
}

/** SessionsSection over real draft state, so a test can follow a sequence of edits. */
function Harness({ saved, onDraft }: { saved: Config; onDraft: (d: Config) => void }) {
  const [draft, setDraftState] = useState(saved);
  const setDraft = (next: Config) => {
    setDraftState(next);
    onDraft(next);
  };
  return (
    <SessionsSection
      config={makeConfig({ saved, draft, setDraft, dirtyFields: dirtyFields(saved, draft) })}
      onNavigate={() => {}}
    />
  );
}

const startIn = () => screen.getByRole("radiogroup", { name: "Start in" });
const checkedStartIn = () =>
  within(startIn())
    .getAllByRole("radio")
    .find((r) => r.getAttribute("aria-checked") === "true")?.textContent;
const pick = (label: string) => fireEvent.click(within(startIn()).getByRole("radio", { name: label }));

describe("SessionsSection", () => {
  beforeEach(() => stubFetch());
  afterEach(() => vi.unstubAllGlobals());

  describe("Start in", () => {
    it("derives the choice from the saved fields, workspace winning when both are set", () => {
      expect(deriveStartIn({ ...fixtureConfig, default_workspace: "ws-1" })).toBe("workspace");
      expect(deriveStartIn({ ...fixtureConfig, default_dir: "/r" })).toBe("folder");
      expect(deriveStartIn({ ...fixtureConfig, default_workspace: "ws-1", default_dir: "/r" })).toBe("workspace");
      expect(deriveStartIn({ ...fixtureConfig, default_workspace: "", default_dir: "" })).toBe("default");
    });

    it("shows the workspace picker for a saved workspace", async () => {
      render(<SessionsSection config={makeConfig({ draft: { ...fixtureConfig, default_workspace: "ws-1" } })} onNavigate={() => {}} />);
      expect(checkedStartIn()).toBe("Workspace");
      await waitFor(() => expect(screen.getByLabelText("Workspace").textContent).toContain("core"));
      expect(screen.queryByLabelText("Folder")).toBeNull();
    });

    it("shows the folder picker for a saved folder", () => {
      render(<SessionsSection config={makeConfig({ draft: { ...fixtureConfig, default_dir: "~/repo/mads" } })} onNavigate={() => {}} />);
      expect(checkedStartIn()).toBe("Folder");
      expect((screen.getByLabelText("Folder") as HTMLInputElement).value).toBe("~/repo/mads");
    });

    it("explains mads default with the Router's launch folder", () => {
      const draft = { ...fixtureConfig, fallback_dir: "/Users/drew/repo/mads" };
      render(<SessionsSection config={makeConfig({ draft })} onNavigate={() => {}} />);
      expect(checkedStartIn()).toBe("mads default");
      const text = screen.getByTestId("start-in-fallback").textContent;
      expect(text).toContain("Sessions start in ~/repo/mads, the folder mads was launched from.");
      expect(text).toContain("Choose Folder to pin a different one.");
      expect(text).toContain("The Launchpad can always pick a folder for a single session.");
    });

    it("says it generically when the Router couldn't report its folder", () => {
      const draft = { ...fixtureConfig, fallback_dir: undefined };
      render(<SessionsSection config={makeConfig({ draft })} onNavigate={() => {}} />);
      expect(screen.getByTestId("start-in-fallback").textContent).toMatch(
        /^Sessions start in the folder mads was launched from\. Choose Folder/,
      );
    });

    it("picking a workspace clears the folder", async () => {
      const onDraft = vi.fn();
      render(<Harness saved={{ ...fixtureConfig, default_dir: "/old" }} onDraft={onDraft} />);
      pick("Workspace");
      // Choosing the segment alone changes nothing yet.
      expect(onDraft).not.toHaveBeenCalled();
      expect(checkedStartIn()).toBe("Workspace");
      fireEvent.click(screen.getByLabelText("Workspace"));
      await waitFor(() => expect(screen.getByRole("option", { name: "core" })).toBeTruthy());
      fireEvent.click(screen.getByRole("option", { name: "core" }));
      expect(onDraft).toHaveBeenLastCalledWith(
        expect.objectContaining({ default_workspace: "ws-1", default_dir: "" }),
      );
    });

    it("typing a folder clears the workspace", () => {
      const onDraft = vi.fn();
      render(<Harness saved={{ ...fixtureConfig, default_workspace: "ws-1" }} onDraft={onDraft} />);
      pick("Folder");
      expect(onDraft).not.toHaveBeenCalled();
      fireEvent.change(screen.getByLabelText("Folder"), { target: { value: "~/repo/x" } });
      expect(onDraft).toHaveBeenLastCalledWith(
        expect.objectContaining({ default_dir: "~/repo/x", default_workspace: "" }),
      );
      expect(checkedStartIn()).toBe("Folder");
    });

    it("mads default clears both at once", () => {
      const onDraft = vi.fn();
      render(<Harness saved={{ ...fixtureConfig, default_workspace: "ws-1", default_dir: "/r" }} onDraft={onDraft} />);
      pick("mads default");
      expect(onDraft).toHaveBeenLastCalledWith(expect.objectContaining({ default_workspace: "", default_dir: "" }));
      expect(screen.getByTestId("start-in-fallback")).toBeTruthy();
    });

    it("re-picking Folder over a legacy both-set config keeps the folder and drops the workspace", () => {
      const onDraft = vi.fn();
      render(<Harness saved={{ ...fixtureConfig, default_workspace: "ws-1", default_dir: "/r" }} onDraft={onDraft} />);
      expect(checkedStartIn()).toBe("Workspace");
      pick("Folder");
      expect(onDraft).toHaveBeenLastCalledWith(expect.objectContaining({ default_dir: "/r", default_workspace: "" }));
    });

    it("an empty segment choice is not an unsaved change", () => {
      const onDraft = vi.fn();
      render(<Harness saved={{ ...fixtureConfig, default_dir: "/r" }} onDraft={onDraft} />);
      pick("Workspace");
      pick("Folder");
      expect(onDraft).not.toHaveBeenCalled();
      expect((screen.getByLabelText("Folder") as HTMLInputElement).value).toBe("/r");
    });
  });

  describe("Subagents", () => {
    it("shows subagents in the session list by default, and turning it off stores the hide flag", () => {
      const setDraft = vi.fn();
      render(<SessionsSection config={makeConfig({ setDraft })} onNavigate={() => {}} />);
      const toggle = screen.getByRole("switch", { name: "Show subagents in the session list" });
      expect(toggle.getAttribute("aria-checked")).toBe("true");
      fireEvent.click(toggle);
      expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ hide_delegates_in_session_list: true }));
    });

    it("hides finished ones after 10 min by default, and stores the choice", () => {
      const setDraft = vi.fn();
      render(<SessionsSection config={makeConfig({ setDraft })} onNavigate={() => {}} />);
      const group = screen.getByRole("radiogroup", { name: "Hide finished ones after" });
      expect(within(group).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Right away", "10 min", "Never"]);
      expect(within(group).getByRole("radio", { name: "10 min" }).getAttribute("aria-checked")).toBe("true");
      fireEvent.click(within(group).getByRole("radio", { name: "Never" }));
      expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ delegate_fade: "never" }));
    });

    it("disables the fade choice while subagents are hidden from the list", () => {
      const draft = { ...fixtureConfig, hide_delegates_in_session_list: true };
      render(<SessionsSection config={makeConfig({ draft })} onNavigate={() => {}} />);
      const group = screen.getByRole("radiogroup", { name: "Hide finished ones after" });
      for (const r of within(group).getAllByRole("radio")) expect((r as HTMLButtonElement).disabled).toBe(true);
    });
  });

  describe("Write commit messages for me", () => {
    it("writes auto_commit_messages when turned on", async () => {
      const setDraft = vi.fn();
      render(<SessionsSection config={makeConfig({ setDraft })} onNavigate={() => {}} />);
      const box = screen.getByRole("switch", { name: "Write commit messages for me" }) as HTMLButtonElement;
      await waitFor(() => expect(box.disabled).toBe(false));
      expect(screen.queryByTestId("auto-commit-unavailable")).toBeNull();
      fireEvent.click(box);
      expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ auto_commit_messages: true }));
    });

    it("is disabled without a Fast Model, with a warning and a link to set one up", async () => {
      stubFetch(false);
      const onNavigate = vi.fn();
      render(<SessionsSection config={makeConfig()} onNavigate={onNavigate} />);
      await waitFor(() =>
        expect(screen.getByTestId("auto-commit-unavailable").textContent).toMatch(/Needs a Fast Model/),
      );
      expect(screen.getByTestId("auto-commit-unavailable").getAttribute("data-tone")).toBe("warn");
      expect((screen.getByRole("switch", { name: "Write commit messages for me" }) as HTMLButtonElement).disabled).toBe(
        true,
      );
      fireEvent.click(screen.getByRole("button", { name: "Set one up →" }));
      expect(onNavigate).toHaveBeenCalledWith("models");
    });

    it("can still be turned off when it was saved on and the Fast Model went away", () => {
      stubFetch(false);
      const setDraft = vi.fn();
      const draft = { ...fixtureConfig, auto_commit_messages: true };
      render(<SessionsSection config={makeConfig({ draft, setDraft })} onNavigate={() => {}} />);
      const box = screen.getByRole("switch", { name: "Write commit messages for me" }) as HTMLButtonElement;
      expect(box.disabled).toBe(false);
      fireEvent.click(box);
      expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ auto_commit_messages: false }));
    });
  });

  it("has no Save button of its own — edits wait for the page's save bar", () => {
    render(<SessionsSection config={makeConfig()} onNavigate={() => {}} />);
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });
});
