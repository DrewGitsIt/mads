import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { HealthDot } from "./HealthDot";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("HealthDot", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reflects the initial healthy state", () => {
    render(<HealthDot capabilityId="c1" health={{ healthy: true }} />);
    expect(screen.getByRole("img", { name: "Healthy" })).toBeTruthy();
  });

  it("reflects unknown health when no health is given", () => {
    render(<HealthDot capabilityId="c1" />);
    expect(screen.getByRole("img", { name: "Health unknown" })).toBeTruthy();
  });

  it("updates on capability.health_changed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            id: "c1",
            name: "github",
            type: "mcp",
            config: {},
            health: { healthy: false, reason: "timeout" },
            created_at: "t",
            updated_at: "t",
          }),
          { status: 200 },
        ),
      ),
    );

    render(<HealthDot capabilityId="c1" health={{ healthy: true }} />);
    expect(screen.getByRole("img", { name: "Healthy" })).toBeTruthy();

    const source = FakeEventSource.instances[0];
    source.emit(
      "capability.health_changed",
      JSON.stringify({ seq: 1, type: "capability.health_changed", actor: "system", payload: {} }),
    );

    await waitFor(() => expect(screen.getByRole("img", { name: "timeout" })).toBeTruthy());
  });
});
