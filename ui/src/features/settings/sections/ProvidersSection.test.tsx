import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProvidersSection } from "./ProvidersSection";
import { fixtureProviderBehaviors, fixtureProviders } from "../../../router/fixtures";

describe("ProvidersSection", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the capability matrix across native/emulated/unsupported", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string) => {
        const url = input.toString();
        if (url.includes("/providers/behaviors")) {
          return Promise.resolve(new Response(JSON.stringify(fixtureProviderBehaviors), { status: 200 }));
        }
        return Promise.resolve(new Response(JSON.stringify(fixtureProviders), { status: 200 }));
      }),
    );

    render(<ProvidersSection />);

    await waitFor(() => expect(screen.getByText("claude-code")).toBeTruthy());
    expect(screen.getByText("fake")).toBeTruthy();
    expect(screen.getAllByText("native").length).toBeGreaterThan(0);
    expect(screen.getAllByText("unsupported").length).toBeGreaterThan(0);
  });
});
