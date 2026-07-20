import { describe, expect, it } from "vitest";
import { applySnapshot, snapshotFromConfig } from "./profileSnapshot";
import { fixtureConfig } from "../../router/fixtures";

describe("snapshotFromConfig", () => {
  it("captures every field except the excluded install-state fields", () => {
    const snapshot = snapshotFromConfig(fixtureConfig);
    expect(snapshot.concurrency_limit).toBe(3);
    expect(snapshot.default_model_tier).toBe("balanced");
    expect(snapshot).not.toHaveProperty("onboarding_complete");
    expect(snapshot).not.toHaveProperty("update_feed_url");
    expect(snapshot).not.toHaveProperty("active_profile_id");
  });
});

describe("applySnapshot", () => {
  it("overlays snapshot fields onto config, leaving unmentioned fields untouched", () => {
    const merged = applySnapshot(fixtureConfig, {
      default_model_tier: "cheapest",
      concurrency_limit: 1,
    });
    expect(merged.default_model_tier).toBe("cheapest");
    expect(merged.concurrency_limit).toBe(1);
    expect(merged.synthesis_mode).toBe(fixtureConfig.synthesis_mode);
  });

  it("never lets a snapshot override an excluded field", () => {
    const merged = applySnapshot(fixtureConfig, {
      onboarding_complete: false,
      active_profile_id: "prf_other",
    } as never);
    expect(merged.onboarding_complete).toBe(fixtureConfig.onboarding_complete);
    expect(merged.active_profile_id).toBe(fixtureConfig.active_profile_id);
  });
});
