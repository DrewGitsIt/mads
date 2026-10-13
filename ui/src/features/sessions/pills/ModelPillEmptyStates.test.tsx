import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ModelPill } from "./ModelPill";
import { resetProviderModelCache } from "../../../router/useProviderModels";

/** Scripts GET /providers plus one provider's models response. */
function stubCatalog(body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(
          new Response(JSON.stringify([{ name: "claude-code", matrix: {} }]), { status: 200 }),
        );
      }
      return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
    }),
  );
}

// An empty model list means three different things, and the menu used to
// collapse all of them into a blank group under the provider name — the
// user could not tell a missing binary from a provider that genuinely
// offers nothing.
describe("ModelPill empty states", () => {
  beforeEach(() => {
    resetProviderModelCache();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("explains an unavailable catalog with the harness's own reason", async () => {
    stubCatalog({ models: [], state: "unavailable", detail: "claude model discovery: not logged in" });

    render(<ModelPill onChange={vi.fn()} />);
    fireEvent.click(screen.getByText("Select model"));

    await waitFor(() =>
      expect(screen.getByText("claude model discovery: not logged in")).toBeTruthy(),
    );
  });

  it("says it is still discovering rather than showing an empty menu", async () => {
    stubCatalog({ models: [], state: "refreshing" });

    render(<ModelPill onChange={vi.fn()} />);
    fireEvent.click(screen.getByText("Select model"));

    await waitFor(() => expect(screen.getByText("discovering models…")).toBeTruthy());
  });

  it("reports a genuinely empty catalog as no models", async () => {
    stubCatalog({ models: [], state: "ready" });

    render(<ModelPill onChange={vi.fn()} />);
    fireEvent.click(screen.getByText("Select model"));

    await waitFor(() => expect(screen.getByText("no models")).toBeTruthy());
  });

  it("falls back to a generic reason when unavailable carries no detail", async () => {
    stubCatalog({ models: [], state: "unavailable" });

    render(<ModelPill onChange={vi.fn()} />);
    fireEvent.click(screen.getByText("Select model"));

    await waitFor(() => expect(screen.getByText("model discovery unavailable")).toBeTruthy());
  });
});
