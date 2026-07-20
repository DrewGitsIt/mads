import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsSurface } from "./SettingsSurface";
import {
  fixtureConfig,
  fixturePreflightChecks,
  fixtureProfiles,
  fixtureProviderBehaviors,
  fixtureProviders,
  fixtureVersionInfo,
} from "../../router/fixtures";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function stubRouter() {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
      if (url.includes("/credentials/env-candidates")) return Promise.resolve(jsonResponse([]));
      if (url.includes("/credentials")) return Promise.resolve(jsonResponse([]));
      if (url.includes("/preflight")) return Promise.resolve(jsonResponse(fixturePreflightChecks));
      if (url.includes("/profiles")) return Promise.resolve(jsonResponse(fixtureProfiles));
      if (url.includes("/providers/behaviors")) return Promise.resolve(jsonResponse(fixtureProviderBehaviors));
      if (url.includes("/providers")) return Promise.resolve(jsonResponse(fixtureProviders));
      if (url.includes("/version")) return Promise.resolve(jsonResponse(fixtureVersionInfo));
      return Promise.resolve(jsonResponse(fixtureConfig));
    }),
  );
}

describe("SettingsSurface", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.location.hash = "";
  });

  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
  });

  it("renders every former panel's section in one scrollable page", async () => {
    stubRouter();
    render(<SettingsSurface />);

    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    expect(screen.getByText("Concurrency limit")).toBeTruthy();
    expect(screen.getByText(/no credentials stored/i)).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Providers" })).toBeTruthy();
    await waitFor(() => expect(screen.getByText(/version/i)).toBeTruthy());
  });

  it("scrolls the section matching a deep-link hash into view on mount", async () => {
    window.location.hash = "#credentials";
    stubRouter();
    render(<SettingsSurface />);

    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());
    const section = document.getElementById("credentials")!;
    expect(section.scrollIntoView).toHaveBeenCalled();
  });
});
