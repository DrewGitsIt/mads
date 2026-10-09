import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GeneralSection } from "./GeneralSection";
import { fixtureConfig, fixturePreflightChecks } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    initial: fixtureConfig,
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyRestartFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

// GeneralSection now fetches the provider list (for the default-provider
// dropdown) alongside preflight. A single shared Response can't back multiple
// fetches — its body is consumed once — so route by URL and return a fresh
// Response per call. Providers includes claude-code so the dropdown has a real
// non-empty option to select.
function stubFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/workspaces")) {
        return Promise.resolve(
          new Response(JSON.stringify([{ id: "ws-1", name: "core", defaults: {} }]), { status: 200 }),
        );
      }
      if (url.includes("/providers/behaviors")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/providers")) {
        return Promise.resolve(
          new Response(JSON.stringify([{ name: "claude-code", matrix: {} }]), { status: 200 }),
        );
      }
      return Promise.resolve(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 }));
    }),
  );
}

describe("GeneralSection", () => {
  beforeEach(() => {
    stubFetch();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the restart-required badge when a restart field is dirty", () => {
    const config = makeConfig({ dirtyRestartFields: ["concurrency_limit"] });
    render(<GeneralSection config={config} />);
    expect(screen.getByText(/restart required/i)).toBeTruthy();
  });

  it("does not show the restart badge when only a live field is dirty", () => {
    const config = makeConfig({ dirtyRestartFields: [] });
    render(<GeneralSection config={config} />);
    expect(screen.queryByText(/restart required/i)).toBeNull();
  });

  it("renders every field error from one submit, not just the first", () => {
    const config = makeConfig({
      fieldErrors: {
        concurrency_limit: "must be at least 1",
        log_retention_days: "must be at least 1",
      },
    });
    render(<GeneralSection config={config} />);
    expect(screen.getAllByText("must be at least 1")).toHaveLength(2);
  });

  it("edits call setDraft with the new value", () => {
    const config = makeConfig();
    render(<GeneralSection config={config} />);
    fireEvent.change(screen.getByLabelText(/concurrency limit/i), { target: { value: "5" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ concurrency_limit: 5 }));
  });

  it("renders default_workspace/default_provider empty when unset", () => {
    render(<GeneralSection config={makeConfig()} />);
    expect((screen.getByLabelText(/default workspace/i) as HTMLSelectElement).value).toBe("");
    expect((screen.getByLabelText(/default folder/i) as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText(/default provider/i) as HTMLSelectElement).value).toBe("");
  });

  it("picks the default workspace from a list, by id, instead of free text", async () => {
    const config = makeConfig();
    render(<GeneralSection config={config} />);
    expect((screen.getByLabelText(/default workspace/i) as HTMLElement).tagName).toBe("SELECT");
    await waitFor(() => expect(screen.getByRole("option", { name: "core" })).toBeTruthy());
    fireEvent.change(screen.getByLabelText(/default workspace/i), { target: { value: "ws-1" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ default_workspace: "ws-1" }));
  });

  it("keeps a saved workspace name selected rather than blanking it", async () => {
    const config = makeConfig({ draft: { ...fixtureConfig, default_workspace: "core" } });
    render(<GeneralSection config={config} />);
    await waitFor(() => expect(screen.getAllByRole("option", { name: "core" }).length).toBeGreaterThan(0));
    expect((screen.getByLabelText(/default workspace/i) as HTMLSelectElement).value).toBe("core");
  });

  it("edits the default folder", () => {
    const config = makeConfig();
    render(<GeneralSection config={config} />);
    fireEvent.change(screen.getByLabelText(/default folder/i), { target: { value: "~/repo/mads" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ default_dir: "~/repo/mads" }));
  });

  it("selecting a default provider from the dropdown calls setDraft", async () => {
    const config = makeConfig();
    render(<GeneralSection config={config} />);
    // The option only exists once the provider list resolves.
    await waitFor(() => expect(screen.getByRole("option", { name: "claude-code" })).toBeTruthy());
    fireEvent.change(screen.getByLabelText(/default provider/i), { target: { value: "claude-code" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ default_provider: "claude-code" }));
  });

  it("display mode and keybindings are dropdowns, not free text", () => {
    render(<GeneralSection config={makeConfig()} />);
    expect((screen.getByLabelText(/display mode/i) as HTMLElement).tagName).toBe("SELECT");
    expect((screen.getByLabelText(/keybindings/i) as HTMLElement).tagName).toBe("SELECT");
    fireEvent.change(screen.getByLabelText(/display mode/i), { target: { value: "compact" } });
    // setDraft is mocked so the value is whatever the fixture had; assert the call shape.
  });

  it("selecting a display mode calls setDraft with the new value", () => {
    const config = makeConfig();
    render(<GeneralSection config={config} />);
    fireEvent.change(screen.getByLabelText(/display mode/i), { target: { value: "compact" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ display_mode: "compact" }));
  });

  it("clicking Save PUTs the updated default_workspace/default_provider", async () => {
    const config = makeConfig({
      draft: { ...fixtureConfig, default_workspace: "ws-1", default_provider: "claude-code" },
    });
    render(<GeneralSection config={config} />);
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(config.save).toHaveBeenCalled());
  });

  it("renders synthesis_debounce_ms and edits call setDraft with the new value", () => {
    const config = makeConfig();
    render(<GeneralSection config={config} />);
    expect((screen.getByLabelText(/synthesis debounce/i) as HTMLInputElement).value).toBe(
      String(fixtureConfig.synthesis_debounce_ms),
    );
    fireEvent.change(screen.getByLabelText(/synthesis debounce/i), { target: { value: "3000" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ synthesis_debounce_ms: 3000 }));
  });

  it("renders preflight checks from usePreflight", async () => {
    render(<GeneralSection config={makeConfig()} />);
    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
  });

  it("renders a failing check's fix_hint", async () => {
    render(<GeneralSection config={makeConfig()} />);
    await waitFor(() => expect(screen.getByText("provider:goose")).toBeTruthy());
    expect(screen.getByText("mads has only been proven against < 1.49.0, detected 1.50.1")).toBeTruthy();
  });

  it("shows subagents in the session list by default, and unticking stores the hide flag", () => {
    const setDraft = vi.fn();
    render(<GeneralSection config={makeConfig({ setDraft })} />);
    const box = screen.getByLabelText("Expose subagents in session list") as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.click(box);
    expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ hide_delegates_in_session_list: true }));
  });

  describe("Write commit messages for me", () => {
    function stubFastModel(providerCompleter: boolean) {
      const base = (globalThis.fetch as unknown as (i: RequestInfo | URL) => Promise<Response>);
      vi.stubGlobal(
        "fetch",
        vi.fn((input: RequestInfo | URL) =>
          String(input).includes("/fast-model/status")
            ? Promise.resolve(
                new Response(
                  JSON.stringify({ mode: "provider", provider_name: "claude-code", provider_completer: providerCompleter }),
                  { status: 200 },
                ),
              )
            : base(input),
        ),
      );
    }

    it("writes auto_commit_messages when ticked", async () => {
      stubFastModel(true);
      const setDraft = vi.fn();
      render(<GeneralSection config={makeConfig({ setDraft })} />);
      const box = screen.getByLabelText("Write commit messages for me") as HTMLInputElement;
      await waitFor(() => expect(box.disabled).toBe(false));
      expect(screen.getByText(/skips the dialog: the Fast Model writes the message/)).toBeTruthy();
      fireEvent.click(box);
      expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ auto_commit_messages: true }));
    });

    it("is disabled, with a hint, without a Fast Model", async () => {
      stubFastModel(false);
      render(<GeneralSection config={makeConfig()} />);
      await waitFor(() =>
        expect(screen.getByTestId("auto-commit-unavailable").textContent).toMatch(/No Fast Model is set up/),
      );
      expect((screen.getByLabelText("Write commit messages for me") as HTMLInputElement).disabled).toBe(true);
    });

    it("can still be turned off when it was saved on and the Fast Model went away", async () => {
      stubFastModel(false);
      const setDraft = vi.fn();
      const draft = { ...fixtureConfig, auto_commit_messages: true };
      render(<GeneralSection config={makeConfig({ draft, setDraft })} />);
      const box = screen.getByLabelText("Write commit messages for me") as HTMLInputElement;
      expect(box.disabled).toBe(false);
      fireEvent.click(box);
      expect(setDraft).toHaveBeenCalledWith(expect.objectContaining({ auto_commit_messages: false }));
    });
  });
});

