import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TrustPill } from "./TrustPill";
import { TRUST_LEVELS } from "./trustLevels";
import { fixtureConfig } from "../../../../router/fixtures";

function stubConfigFetch(overrides: Partial<typeof fixtureConfig> = {}) {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify({ ...fixtureConfig, ...overrides }), { status: 200 }));
  vi.stubGlobal("fetch", fetchMock);
}

describe("TrustPill", () => {
  it("shows the resolved default trust level when unset", async () => {
    stubConfigFetch({ default_trust: "limited" });
    render(<TrustPill onChange={() => {}} />);
    await waitFor(() => expect(screen.getByText(/Limited \(default\)/)).toBeTruthy());
    vi.unstubAllGlobals();
  });

  it("reports the selected trust level", async () => {
    stubConfigFetch({ default_trust: "limited" });
    const onChange = vi.fn();
    render(<TrustPill onChange={onChange} />);
    await waitFor(() => expect(screen.getByText(/Limited \(default\)/)).toBeTruthy());
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    fireEvent.click(screen.getByText("Full access"));
    expect(onChange).toHaveBeenCalledWith("full");
    vi.unstubAllGlobals();
  });

  it("renders an accurate one-line description for every trust option", async () => {
    stubConfigFetch({ default_trust: "limited" });
    render(<TrustPill onChange={() => {}} />);
    await waitFor(() => expect(screen.getByText(/Limited \(default\)/)).toBeTruthy());
    fireEvent.click(screen.getByText(/Limited \(default\)/));

    for (const level of TRUST_LEVELS) {
      expect(screen.getByText(level.description)).toBeTruthy();
    }
    vi.unstubAllGlobals();
  });
});
