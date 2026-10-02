import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CredentialsSection } from "./CredentialsSection";
import { fixtureConfig, fixtureProfile } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";
import type { Profile } from "../../../router/types";

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

function routedFetch(routes: Record<string, () => Response>) {
  return vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
    const url = input.toString();
    const method = init?.method ?? "GET";
    for (const [key, respond] of Object.entries(routes)) {
      const [routeMethod, routePath] = key.split(" ");
      if (method === routeMethod && url.includes(routePath)) {
        return Promise.resolve(respond());
      }
    }
    throw new Error(`unhandled fetch: ${method} ${url}`);
  });
}

describe("CredentialsSection", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never renders a credential value after save, and clears the field synchronously", async () => {
    const secret = "sk-live-do-not-render-this-value";
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials/env-candidates": () => jsonResponse({ candidates: [], looked_for: [] }),
        "GET /credentials": () => jsonResponse([]),
        "POST /credentials": () => jsonResponse({ name: "OPENAI_API_KEY" }, 201),
      }),
    );

    render(<CredentialsSection config={makeConfig()} profiles={[]} />);
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "OPENAI_API_KEY" } });
    fireEvent.change(screen.getByLabelText(/value/i), { target: { value: secret } });
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));

    // Synchronous half of the invariant: the value field is cleared in the
    // same event-handler tick as the click, not after the POST resolves.
    expect((screen.getByLabelText(/value/i) as HTMLInputElement).value).toBe("");

    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());
    expect(document.body.innerHTML).not.toContain(secret);
  });

  it("env-import flow only ever moves names, never values", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials/env-candidates": () =>
          jsonResponse({ candidates: ["ANTHROPIC_API_KEY"], looked_for: ["AWS_*"] }),
        "GET /credentials": () => jsonResponse([]),
        "POST /credentials/import": () => jsonResponse({ imported: ["ANTHROPIC_API_KEY"] }),
      }),
    );

    render(<CredentialsSection config={makeConfig()} profiles={[]} />);
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /scan environment/i }));
    await waitFor(() => expect(screen.getByText("ANTHROPIC_API_KEY")).toBeTruthy());

    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /import selected/i }));

    await waitFor(() => expect(screen.getAllByText("ANTHROPIC_API_KEY")).toHaveLength(1));
  });

  it("deletes a stored credential after confirmation", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY"]),
        "DELETE /credentials": () => new Response(null, { status: 204 }),
      }),
    );

    render(<CredentialsSection config={makeConfig()} profiles={[]} />);
    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /delete/i }));
    const dialog = screen.getByRole("alertdialog");
    // still present until confirmed
    expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("button", { name: /^delete$/i }));
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());
  });

  it("delete confirmation names the profiles bound to the credential", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY"]),
        "DELETE /credentials": () => new Response(null, { status: 204 }),
      }),
    );
    const boundProfile: Profile = {
      ...fixtureProfile,
      id: "prf_bound",
      name: "bound-profile",
      snapshot: { credential_bindings: ["OPENAI_API_KEY"] },
    };

    render(<CredentialsSection config={makeConfig()} profiles={[boundProfile]} />);
    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /delete/i }));
    expect(screen.getByText(/bound-profile/i)).toBeTruthy();
  });

  it("names a profile with no credential_bindings set, since undefined means all credentials are active", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY"]),
        "DELETE /credentials": () => new Response(null, { status: 204 }),
      }),
    );
    const untouchedProfile: Profile = {
      ...fixtureProfile,
      id: "prf_untouched",
      name: "untouched-profile",
      snapshot: {},
    };

    render(<CredentialsSection config={makeConfig()} profiles={[untouchedProfile]} />);
    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /delete/i }));
    expect(screen.getByText(/untouched-profile/i)).toBeTruthy();
  });

  it("lists every stored credential, dimming ones not bound in the draft", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY", "GITHUB_TOKEN"]),
      }),
    );
    const config = makeConfig({
      draft: { ...fixtureConfig, credential_bindings: ["OPENAI_API_KEY"] },
    });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByText("GITHUB_TOKEN")).toBeTruthy());

    expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy();
    expect(screen.getByText("Inactive")).toBeTruthy();
  });

  it("toggling a credential edits draft.credential_bindings instead of calling the vault", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY", "GITHUB_TOKEN"]),
      }),
    );
    const config = makeConfig({
      draft: { ...fixtureConfig, credential_bindings: ["OPENAI_API_KEY"] },
    });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByText("GITHUB_TOKEN")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /use in this profile/i }));

    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ credential_bindings: ["OPENAI_API_KEY", "GITHUB_TOKEN"] }),
    );
  });

  it("defaults to inherit mode with an accurate credential-count summary, and hides passthrough chips", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY"]),
      }),
    );
    const config = makeConfig({ draft: { ...fixtureConfig, credential_bindings: undefined } });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());

    expect(screen.getByText(/inherited environment \+ 1 mads credential/i)).toBeTruthy();
    expect(screen.queryByLabelText(/passthrough env vars/i)).toBeNull();
  });

  it("switching to isolated mode edits draft.env_mode and reveals the passthrough editor", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY", "GITHUB_TOKEN"]),
      }),
    );
    const config = makeConfig({
      draft: { ...fixtureConfig, credential_bindings: ["OPENAI_API_KEY"], env_mode: "isolated", env_passthrough: ["AWS_PROFILE"] },
    });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());

    expect(
      screen.getByText(/isolated: base environment \+ 1 credential \+ 1 passthrough \(AWS_PROFILE\)/i),
    ).toBeTruthy();
    expect(screen.getByText("AWS_PROFILE")).toBeTruthy();
  });

  it("clicking the isolated chip edits draft.env_mode", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse([]),
      }),
    );
    const config = makeConfig({ draft: { ...fixtureConfig, credential_bindings: undefined } });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "isolated" }));

    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ env_mode: "isolated" }));
  });

  it("adds a passthrough name to draft.env_passthrough and clears the input", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse([]),
      }),
    );
    const config = makeConfig({
      draft: { ...fixtureConfig, credential_bindings: undefined, env_mode: "isolated", env_passthrough: [] },
    });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByLabelText(/passthrough env vars/i)).toBeTruthy());

    fireEvent.change(screen.getByLabelText(/passthrough env vars/i), { target: { value: "AWS_PROFILE" } });
    fireEvent.click(screen.getByRole("button", { name: /add passthrough/i }));

    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ env_passthrough: ["AWS_PROFILE"] }),
    );
    expect((screen.getByLabelText(/passthrough env vars/i) as HTMLInputElement).value).toBe("");
  });

  it("removes a passthrough name via its chip's remove button", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse([]),
      }),
    );
    const config = makeConfig({
      draft: {
        ...fixtureConfig,
        credential_bindings: undefined,
        env_mode: "isolated",
        env_passthrough: ["AWS_PROFILE"],
      },
    });

    render(<CredentialsSection config={config} profiles={[]} />);
    await waitFor(() => expect(screen.getByText("AWS_PROFILE")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /remove aws_profile/i }));

    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ env_passthrough: [] }));
  });
});
