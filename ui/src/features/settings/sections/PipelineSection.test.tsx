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

function stubCredentials(names: string[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
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

  it("shows the Fast Model group dormant when no endpoint is configured", () => {
    stubCredentials([]);
    render(<PipelineSection config={makeConfig()} />);
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
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    fireEvent.change(screen.getByLabelText(/endpoint/i), {
      target: { value: "https://api.example.com/v1" },
    });
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ fast_model_base_url: "https://api.example.com/v1" }),
    );
  });

  it("lists stored credentials in the Fast Model credential picker", async () => {
    stubCredentials(["OPENAI_API_KEY"]);
    render(<PipelineSection config={makeConfig()} />);
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "OPENAI_API_KEY" })).toBeTruthy(),
    );
  });
});
