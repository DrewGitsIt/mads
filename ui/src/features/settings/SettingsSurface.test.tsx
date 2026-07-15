import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SettingsSurface } from "./SettingsSurface";
import { fixtureConfig, fixturePreflightChecks } from "../../router/fixtures";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function stubRouter() {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
      if (url.includes("/credentials")) return Promise.resolve(jsonResponse([]));
      if (url.includes("/preflight")) return Promise.resolve(jsonResponse(fixturePreflightChecks));
      return Promise.resolve(jsonResponse(fixtureConfig));
    }),
  );
}

describe("SettingsSurface", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("defaults to the Appearance panel", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(fixtureConfig))));
    render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    expect(screen.getByText("clay")).toBeTruthy();
  });

  it("switching nav sections renders the corresponding panel, sharing one useConfig instance", async () => {
    stubRouter();
    render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());

    fireEvent.click(screen.getByText("Credentials"));
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());

    fireEvent.click(screen.getByText("General"));
    await waitFor(() => expect(screen.getByLabelText(/concurrency limit/i)).toBeTruthy());
  });
});
