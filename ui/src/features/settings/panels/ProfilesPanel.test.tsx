import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProfilesPanel } from "./ProfilesPanel";
import { fixtureConfig, fixtureProfile, fixtureProfiles } from "../../../router/fixtures";
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

describe("ProfilesPanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the profile list", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(fixtureProfiles))));
    render(<ProfilesPanel config={makeConfig()} />);
    await waitFor(() => expect(screen.getByText(fixtureProfile.name)).toBeTruthy());
  });

  it("hides Delete for a system profile but keeps it for a user profile", async () => {
    const systemProfile = { ...fixtureProfile, id: "prf_sys", name: "default", system: true };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(jsonResponse([systemProfile, fixtureProfile]))),
    );
    render(<ProfilesPanel config={makeConfig()} />);
    await waitFor(() => expect(screen.getByText("default")).toBeTruthy());
    expect(screen.getAllByRole("button", { name: /delete/i })).toHaveLength(1);
  });

  it("creates a profile via the create form", async () => {
    const fetchMock = vi.fn().mockImplementation((_input: URL | string, init?: RequestInit) => {
      if (init?.method === "POST") return Promise.resolve(jsonResponse({ ...fixtureProfile, id: "prf_new" }, 201));
      return Promise.resolve(jsonResponse(fixtureProfiles));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<ProfilesPanel config={makeConfig()} />);
    await waitFor(() => expect(screen.getByText(fixtureProfile.name)).toBeTruthy());

    fireEvent.change(screen.getByLabelText(/new profile name/i), { target: { value: "focus-mode" } });
    fireEvent.click(screen.getByRole("button", { name: /create profile/i }));

    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === "POST")).toBe(
        true,
      ),
    );
  });

  it("activate() feeds the merged config back into the shared useConfig instance", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
      if (url.includes("/activate")) return Promise.resolve(jsonResponse(fixtureConfig));
      return Promise.resolve(jsonResponse(fixtureProfiles));
    });
    vi.stubGlobal("fetch", fetchMock);
    const config = makeConfig();
    render(<ProfilesPanel config={config} />);
    await waitFor(() => expect(screen.getByText(fixtureProfile.name)).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /activate/i }));

    await waitFor(() => expect(config.setDraft).toHaveBeenCalledWith(fixtureConfig));
    expect(config.refetch).toHaveBeenCalled();
  });
});
