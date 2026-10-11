import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PipelineSection } from "./PipelineSection";
import { fixtureConfig } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    initial: fixtureConfig,
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyRestartFields: [],
    dirtyFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    saveFields: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function stubCredentials(
  names: string[],
  fastModelStatus: unknown = { mode: "provider", provider_name: "claude-code", provider_completer: true },
) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
      if (url.includes("/fast-model/status")) return Promise.resolve(jsonResponse(fastModelStatus));
      if (url.includes("/credentials/env-candidates"))
        return Promise.resolve(jsonResponse({ candidates: [], looked_for: [] }));
      if (url.includes("/credentials")) return Promise.resolve(jsonResponse(names));
      throw new Error(`unhandled fetch: ${url}`);
    }),
  );
}

describe("PipelineSection", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows a static cost hint for the current synthesis mode", () => {
    stubCredentials([]);
    render(<PipelineSection config={makeConfig()} />);
    expect(screen.getByText(/summary only/i)).toBeTruthy();
  });

  it("changing synthesis mode updates the draft and its cost hint", () => {
    stubCredentials([]);
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    const group = screen.getByRole("radiogroup", { name: /synthesis mode/i });
    // Labels are humanized; the stored values stay off/light/full.
    expect(within(group).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Off", "Summary", "Summary + DPA"]);
    fireEvent.click(within(group).getByRole("radio", { name: "Summary + DPA" }));
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ synthesis_mode: "full" }));
  });

  it("toggling auto-approve synthesis updates the draft", () => {
    stubCredentials([]);
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    fireEvent.click(screen.getByLabelText(/auto-approve synthesis/i));
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ synthesis_auto_approve: true }),
    );
  });

  it("checks prevent-sleep when allow_sleep_during_pipelines is false", () => {
    stubCredentials([]);
    render(<PipelineSection config={makeConfig()} />);
    const toggle = screen.getByRole("switch", { name: /prevent system sleep while a pipeline is running/i });
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    expect(screen.getByText("Changes apply to runs started afterward.")).toBeTruthy();
  });

  it("unchecks prevent-sleep when allow_sleep_during_pipelines is true", () => {
    stubCredentials([]);
    const draft = { ...fixtureConfig, allow_sleep_during_pipelines: true };
    render(<PipelineSection config={makeConfig({ draft })} />);
    const toggle = screen.getByRole("switch", { name: /prevent system sleep/i });
    expect(toggle.getAttribute("aria-checked")).toBe("false");
  });

  it("unchecking prevent-sleep writes allow_sleep_during_pipelines: true", () => {
    stubCredentials([]);
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    fireEvent.click(screen.getByLabelText(/prevent system sleep/i));
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ allow_sleep_during_pipelines: true }),
    );
  });

  it.each([
    ["darwin", "Closing the lid on battery still sleeps your Mac. The run pauses and recovers when it wakes."],
    ["linux", "Closing a laptop lid may still suspend, depending on your desktop's lid settings."],
  ])("shows the %s sleep caveat", (platform, caveat) => {
    stubCredentials([]);
    render(<PipelineSection config={makeConfig({ draft: { ...fixtureConfig, platform } })} />);
    expect(screen.getByText(caveat)).toBeTruthy();
  });

  it("shows no sleep caveat on other platforms", () => {
    stubCredentials([]);
    render(<PipelineSection config={makeConfig({ draft: { ...fixtureConfig, platform: "windows" } })} />);
    expect(screen.queryByText(/closing (the|a laptop) lid/i)).toBeNull();
  });

  it("renders every field error from one submit", () => {
    stubCredentials([]);
    const config = makeConfig({
      fieldErrors: {
        synthesis_mode: "invalid",
        default_model_tier: "invalid",
      },
    });
    render(<PipelineSection config={config} />);
    expect(screen.getAllByText("invalid")).toHaveLength(2);
  });

  it("changing the stage timeout updates the draft", () => {
    stubCredentials([]);
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    fireEvent.change(screen.getByLabelText(/stage timeout/i), { target: { value: "45" } });
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ pipeline_stage_timeout_minutes: 45 }),
    );
  });

  it("shows the stage timeout field error", () => {
    stubCredentials([]);
    const config = makeConfig({
      fieldErrors: { pipeline_stage_timeout_minutes: "must not be negative" },
    });
    render(<PipelineSection config={config} />);
    expect(screen.getByText("must not be negative")).toBeTruthy();
  });

  it("shows the Fast Model group dormant in endpoint mode when no endpoint is configured", () => {
    stubCredentials([]);
    const config = makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } });
    render(<PipelineSection config={config} />);
    expect(screen.getByText(/dormant/i)).toBeTruthy();
  });

  it("shows the Fast Model group active once an endpoint is set", () => {
    stubCredentials([]);
    const config = makeConfig({
      draft: { ...fixtureConfig, fast_model_base_url: "https://api.example.com/v1" },
    });
    render(<PipelineSection config={config} />);
    expect(screen.getByText(/fast model is active/i)).toBeTruthy();
  });

  it("editing the endpoint updates the draft", () => {
    stubCredentials([]);
    const config = makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } });
    render(<PipelineSection config={config} />);
    fireEvent.change(screen.getByLabelText(/endpoint/i), {
      target: { value: "https://api.example.com/v1" },
    });
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ fast_model_base_url: "https://api.example.com/v1" }),
    );
  });

  it("defaults to provider mode and reports active when the provider supports Completer", async () => {
    stubCredentials([], { mode: "provider", provider_name: "claude-code", provider_completer: true });
    render(<PipelineSection config={makeConfig()} />);
    const backend = screen.getByRole("radiogroup", { name: /backend/i });
    expect(within(backend).getByRole("radio", { name: "My provider" }).getAttribute("aria-checked")).toBe("true");
    await waitFor(() => expect(screen.getByText(/running via claude-code/i)).toBeTruthy());
  });

  it("shows the exact no-Completer status line in provider mode", async () => {
    stubCredentials([], { mode: "provider", provider_name: "kiro-cli", provider_completer: false });
    render(<PipelineSection config={makeConfig()} />);
    await waitFor(() =>
      expect(
        screen.getByText(
          "Your provider doesn't support background completions — configure an endpoint or switch provider.",
        ),
      ).toBeTruthy(),
    );
  });

  it("hides endpoint/model/credential fields outside endpoint mode", () => {
    stubCredentials([]);
    render(<PipelineSection config={makeConfig()} />);
    expect(screen.queryByLabelText(/^endpoint$/i)).toBeNull();
  });

  it("switching the backend selector to Off updates the draft", () => {
    stubCredentials([]);
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    fireEvent.click(within(screen.getByRole("radiogroup", { name: /backend/i })).getByRole("radio", { name: "Off" }));
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ fast_model_mode: "off" }));
  });

  it("lists stored credentials in the Fast Model credential picker", async () => {
    stubCredentials(["OPENAI_API_KEY"]);
    const config = makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } });
    render(<PipelineSection config={config} />);
    fireEvent.click(screen.getByLabelText(/credential/i));
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "OPENAI_API_KEY" })).toBeTruthy(),
    );
  });

  it("shows the cost alert in dollars and stores cents", () => {
    stubCredentials([]);
    const config = makeConfig({ draft: { ...fixtureConfig, cost_alert_cents: 250 } });
    render(<PipelineSection config={config} />);
    const input = screen.getByLabelText(/cost alert/i) as HTMLInputElement;
    expect(input.value).toBe("2.5");
    fireEvent.change(input, { target: { value: "1.75" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ cost_alert_cents: 175 }));
  });
});
