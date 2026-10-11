import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspaceStep } from "./WorkspaceStep";
import { fixtureConfig, fixtureProviders } from "../../router/fixtures";
import type { UseConfig } from "../../features/settings/state/useConfig";

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

describe("WorkspaceStep", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a workspace and advances on success", async () => {
    const fetchMock = routedFetch({
      "GET /providers": fixtureProviders,
      "POST /workspaces": { id: "ws-1", name: "mads" },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    const config = makeConfig();
    render(<WorkspaceStep onNext={onNext} onBack={vi.fn()} config={config} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    const call = fetchMock.mock.calls.find((args: unknown[]) =>
      (args[0] as string | URL).toString().includes("/workspaces"),
    );
    const [, init] = call as [string | URL, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ name: "mads", repos: [{ path: "/repo", primary: true }] });
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ default_workspace: "ws-1" }),
    );
  });

  it("stages the chosen provider into config.draft alongside the workspace id", async () => {
    const fetchMock = routedFetch({
      "GET /providers": fixtureProviders,
      "POST /workspaces": { id: "ws-1", name: "mads" },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    const config = makeConfig();
    render(<WorkspaceStep onNext={onNext} onBack={vi.fn()} config={config} />);

    await waitFor(() => expect(screen.getByLabelText(/default provider/i)).toBeTruthy());
    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.change(screen.getByLabelText(/default provider/i), { target: { value: "claude-code" } });
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ default_workspace: "ws-1", default_provider: "claude-code" }),
    );
  });

  it("does not stage default_provider when left at its no-default placeholder", async () => {
    const fetchMock = routedFetch({
      "GET /providers": fixtureProviders,
      "POST /workspaces": { id: "ws-1", name: "mads" },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    const config = makeConfig({ draft: { ...fixtureConfig, default_provider: "fake" } });
    render(<WorkspaceStep onNext={onNext} onBack={vi.fn()} config={config} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    const [draftArg] = (config.setDraft as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(draftArg.default_provider).toBe("fake");
    expect(draftArg.default_workspace).toBe("ws-1");
  });

  it("shows an inline error and does not advance on failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
        const method = init?.method ?? "GET";
        const path = new URL(input.toString()).pathname;
        if (method === "GET" && path === "/providers") {
          return Promise.resolve(new Response(JSON.stringify(fixtureProviders), { status: 200 }));
        }
        return Promise.resolve(new Response("boom", { status: 500 }));
      }),
    );
    const onNext = vi.fn();
    render(<WorkspaceStep onNext={onNext} onBack={vi.fn()} config={makeConfig()} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(screen.getByText(/couldn't create the workspace/i)).toBeTruthy());
    expect(onNext).not.toHaveBeenCalled();
  });
});
