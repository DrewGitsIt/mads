import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CredentialsPanel } from "./CredentialsPanel";

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

describe("CredentialsPanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never renders a credential value after save, and clears the field synchronously", async () => {
    const secret = "sk-live-do-not-render-this-value";
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials/env-candidates": () => jsonResponse([]),
        "GET /credentials": () => jsonResponse([]),
        "POST /credentials": () => jsonResponse({ name: "OPENAI_API_KEY" }, 201),
      }),
    );

    render(<CredentialsPanel />);
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
        "GET /credentials/env-candidates": () => jsonResponse(["ANTHROPIC_API_KEY"]),
        "GET /credentials": () => jsonResponse([]),
        "POST /credentials/import": () => jsonResponse({ imported: ["ANTHROPIC_API_KEY"] }),
      }),
    );

    render(<CredentialsPanel />);
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /scan environment/i }));
    await waitFor(() => expect(screen.getByText("ANTHROPIC_API_KEY")).toBeTruthy());

    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /import selected/i }));

    await waitFor(() => expect(screen.getAllByText("ANTHROPIC_API_KEY")).toHaveLength(1));
  });

  it("deletes a stored credential", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": () => jsonResponse(["OPENAI_API_KEY"]),
        "DELETE /credentials": () => new Response(null, { status: 204 }),
      }),
    );

    render(<CredentialsPanel />);
    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /delete/i }));
    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());
  });
});
