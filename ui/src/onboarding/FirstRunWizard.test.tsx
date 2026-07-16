import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FirstRunWizard } from "./FirstRunWizard";
import { fixtureConfig, fixturePreflightChecks, fixtureProviders } from "../router/fixtures";

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

function putConfigBody(fetchMock: ReturnType<typeof vi.fn>) {
  const call = fetchMock.mock.calls.find((args: unknown[]) => {
    const [url, init] = args as [string | URL, RequestInit?];
    return init?.method === "PUT" && new URL(url.toString()).pathname === "/config";
  });
  const [, init] = call as [string | URL, RequestInit];
  return JSON.parse(init.body as string);
}

describe("FirstRunWizard", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("completes the full 5-step flow against a clean data dir", async () => {
    const fetchMock = routedFetch({
      "GET /config": { ...fixtureConfig, onboarding_complete: false },
      "GET /preflight": fixturePreflightChecks,
      "GET /credentials": [],
      "GET /credentials/env-candidates": [],
      "GET /providers": fixtureProviders,
      "POST /workspaces": { id: "ws-1", name: "mads" },
      "PUT /config": { ...fixtureConfig, onboarding_complete: true },
    });
    vi.stubGlobal("fetch", fetchMock);
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
    fireEvent.change(screen.getByLabelText(/default provider/i), { target: { value: "claude-code" } });
    fireEvent.click(screen.getByText("Create workspace"));

    // 5. Done
    await waitFor(() => expect(screen.getByText("Go to Launchpad")).toBeTruthy());
    fireEvent.click(screen.getByText("Go to Launchpad"));

    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));

    const body = putConfigBody(fetchMock);
    expect(body.default_workspace).toBe("ws-1");
    expect(body.default_provider).toBe("claude-code");
    expect(body.onboarding_complete).toBe(true);
  });

  it("skipping the provider choice leaves default_provider untouched", async () => {
    const fetchMock = routedFetch({
      "GET /config": { ...fixtureConfig, onboarding_complete: false, default_provider: "fake" },
      "GET /preflight": fixturePreflightChecks,
      "GET /credentials": [],
      "GET /credentials/env-candidates": [],
      "GET /providers": fixtureProviders,
      "POST /workspaces": { id: "ws-1", name: "mads" },
      "PUT /config": { ...fixtureConfig, onboarding_complete: true, default_provider: "fake" },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onComplete = vi.fn();
    render(<FirstRunWizard onComplete={onComplete} />);

    await waitFor(() => expect(screen.getByText("Get started")).toBeTruthy());
    fireEvent.click(screen.getByText("Get started"));

    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
    fireEvent.click(screen.getByText("Continue"));

    await waitFor(() => expect(screen.getByText(/no unimported env vars/i)).toBeTruthy());
    fireEvent.click(screen.getByText("Skip for now"));

    await waitFor(() => expect(screen.getByLabelText(/name/i)).toBeTruthy());
    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    // Leave the provider select at "" — the skip case.
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(screen.getByText("Go to Launchpad")).toBeTruthy());
    fireEvent.click(screen.getByText("Go to Launchpad"));

    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));

    const body = putConfigBody(fetchMock);
    expect(body.default_workspace).toBe("ws-1");
    expect(body.default_provider).toBe("fake");
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
