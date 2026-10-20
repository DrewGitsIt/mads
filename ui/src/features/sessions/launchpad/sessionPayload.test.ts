import { describe, expect, it } from "vitest";
import { buildCreateSessionPayload } from "./sessionPayload";

describe("buildCreateSessionPayload trust", () => {
  it("sends the staged trust, or none, on a provider with a choice", () => {
    expect(buildCreateSessionPayload("go", { trust: "limited" }).trust).toBe("limited");
    expect(buildCreateSessionPayload("go", {}).trust).toBeUndefined();
  });

  it("sends Full on a full-trust-only provider, whatever was staged", () => {
    for (const trust of [undefined, "limited", "read_only", "auto_review", "full"] as const) {
      expect(buildCreateSessionPayload("go", { trust }, { fullTrustOnly: true }).trust).toBe("full");
    }
  });
});
