import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
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
      if (url.includes("/credentials/env-candidates")) return Promise.resolve(jsonResponse([]));
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
    fireEvent.change(screen.getByLabelText(/synthesis mode/i), { target: { value: "full" } });
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
    expect((screen.getByLabelText(/backend/i) as HTMLSelectElement).value).toBe("provider");
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
    fireEvent.change(screen.getByLabelText(/backend/i), { target: { value: "off" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ fast_model_mode: "off" }));
  });

  it("lists stored credentials in the Fast Model credential picker", async () => {
    stubCredentials(["OPENAI_API_KEY"]);
    const config = makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } });
    render(<PipelineSection config={config} />);
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "OPENAI_API_KEY" })).toBeTruthy(),
    );
  });
});
