import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FirstRunWizard } from "./FirstRunWizard";
import { fixtureConfig, fixturePreflightChecks } from "../router/fixtures";

function routedFetch(routes: Record<string, unknown>) {
  return vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const path = new URL(input.toString()).pathname;
    const key = `${method} ${path}`;
    const body = routes[key];
    if (body === undefined) throw new Error(`unstubbed route: ${key}`);
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
}

describe("FirstRunWizard", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("completes the full 5-step flow against a clean data dir", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /config": { ...fixtureConfig, onboarding_complete: false },
        "GET /preflight": fixturePreflightChecks,
        "GET /credentials": [],
        "GET /credentials/env-candidates": [],
        "POST /workspaces": { id: "ws-1", name: "mads" },
        "PUT /config": { ...fixtureConfig, onboarding_complete: true },
      }),
    );
    const onComplete = vi.fn();
    render(<FirstRunWizard onComplete={onComplete} />);

    // 1. Welcome
    await waitFor(() => expect(screen.getByText("Get started")).toBeTruthy());
    fireEvent.click(screen.getByText("Get started"));

    // 2. Provider preflight — StubChecker reports healthy, Continue always enabled
    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
    fireEvent.click(screen.getByText("Continue"));

    // 3. Credential import — skip
    await waitFor(() => expect(screen.getByText(/no unimported env vars/i)).toBeTruthy());
    fireEvent.click(screen.getByText("Skip for now"));

    // 4. First workspace
    await waitFor(() => expect(screen.getByLabelText(/name/i)).toBeTruthy());
    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.click(screen.getByText("Create workspace"));

    // 5. Done
    await waitFor(() => expect(screen.getByText("Go to Launchpad")).toBeTruthy());
    fireEvent.click(screen.getByText("Go to Launchpad"));

    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
  });

  it("back navigates to the previous step", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /config": { ...fixtureConfig, onboarding_complete: false },
        "GET /preflight": fixturePreflightChecks,
      }),
    );
    render(<FirstRunWizard onComplete={vi.fn()} />);

    fireEvent.click(screen.getByText("Get started"));
    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
    fireEvent.click(screen.getByText("Back"));

    await waitFor(() => expect(screen.getByText("Get started")).toBeTruthy());
  });
});
