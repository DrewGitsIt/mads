import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CredentialImportStep } from "./CredentialImportStep";

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

describe("CredentialImportStep", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists candidates and imports only the selected names", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": [],
        "GET /credentials/env-candidates": ["OPENAI_API_KEY", "ANTHROPIC_API_KEY"],
        "POST /credentials/import": { imported: ["OPENAI_API_KEY"] },
      }),
    );
    const onNext = vi.fn();
    render(<CredentialImportStep onNext={onNext} onBack={vi.fn()} />);

    await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("OPENAI_API_KEY"));
    fireEvent.click(screen.getByText("Import selected"));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
  });

  it("Skip for now calls onNext without importing", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /credentials": [],
        "GET /credentials/env-candidates": [],
      }),
    );
    const onNext = vi.fn();
    render(<CredentialImportStep onNext={onNext} onBack={vi.fn()} />);
    await waitFor(() => expect(screen.getByText(/no unimported env vars/i)).toBeTruthy());
    fireEvent.click(screen.getByText("Skip for now"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });
});
