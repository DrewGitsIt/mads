import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useDrift } from "./useDrift";
import type { UseConfig } from "./useConfig";
import type { UseProfiles } from "./useProfiles";
import { fixtureConfig, fixtureProfile } from "../../../router/fixtures";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  const draft = { ...fixtureConfig, active_profile_id: fixtureProfile.id };
  return {
    initial: draft,
    saved: draft,
    draft,
    loading: false,
    fieldErrors: {},
    dirtyRestartFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function makeProfiles(overrides: Partial<UseProfiles> = {}): UseProfiles {
  return {
    profiles: [fixtureProfile],
    loading: false,
    create: vi.fn().mockResolvedValue({ ...fixtureProfile, id: "prf_new" }),
    update: vi.fn().mockResolvedValue(fixtureProfile),
    remove: vi.fn().mockResolvedValue(undefined),
    activate: vi.fn().mockResolvedValue(fixtureConfig),
    ...overrides,
  };
}

describe("useDrift", () => {
  it("is not drifted when no profile is active", () => {
    const config = makeConfig({
      initial: fixtureConfig,
      saved: fixtureConfig,
      draft: fixtureConfig,
    });
    const { result } = renderHook(() => useDrift(config, makeProfiles({ profiles: [] }), []));
    expect(result.current.isDrifted).toBe(false);
    expect(result.current.activeProfile).toBeUndefined();
  });

  it("detects drift when the draft diverges from the active profile's snapshot", () => {
    const config = makeConfig();
    const { result } = renderHook(() => useDrift(config, makeProfiles(), []));
    expect(result.current.isDrifted).toBe(true);
    expect(result.current.fieldDiff.map((e) => e.field).sort()).toEqual([
      "concurrency_limit",
      "default_model_tier",
    ]);
  });

  it("detects credential-toggle drift via credential_bindings", () => {
    const config = makeConfig({
      draft: {
        ...fixtureConfig,
        active_profile_id: fixtureProfile.id,
        default_model_tier: "cheapest",
        concurrency_limit: 1,
        credential_bindings: ["anthropic"],
      },
    });
    const profileWithBindings = {
      ...fixtureProfile,
      snapshot: { ...fixtureProfile.snapshot, credential_bindings: ["openai"] },
    };
    const { result } = renderHook(() =>
      useDrift(config, makeProfiles({ profiles: [profileWithBindings] }), ["anthropic", "openai"]),
    );
    expect(result.current.isDrifted).toBe(true);
    expect(result.current.credentialDiff).toEqual([
      { name: "anthropic", wasActive: true, willBeActive: false },
      { name: "openai", wasActive: false, willBeActive: true },
    ]);
  });

  it("is not drifted once the draft matches the profile snapshot", () => {
    const config = makeConfig({
      draft: { ...fixtureConfig, active_profile_id: fixtureProfile.id, default_model_tier: "cheapest", concurrency_limit: 1 },
    });
    const { result } = renderHook(() => useDrift(config, makeProfiles(), []));
    expect(result.current.isDrifted).toBe(false);
  });

  it("revert() applies the profile snapshot onto the draft and saves it", async () => {
    const save = vi.fn().mockResolvedValue(true);
    const config = makeConfig({ save });
    const { result } = renderHook(() => useDrift(config, makeProfiles(), []));

    await act(async () => {
      await result.current.revert();
    });

    expect(save).toHaveBeenCalledWith(
      expect.objectContaining({ default_model_tier: "cheapest", concurrency_limit: 1 }),
    );
  });

  it("updateProfile() saves the draft then recaptures it into the profile's snapshot", async () => {
    const save = vi.fn().mockResolvedValue(true);
    const config = makeConfig({ save });
    const update = vi.fn().mockResolvedValue(fixtureProfile);
    const { result } = renderHook(() => useDrift(config, makeProfiles({ update }), []));

    await act(async () => {
      await result.current.updateProfile();
    });

    expect(save).toHaveBeenCalledWith(config.draft);
    expect(update).toHaveBeenCalledWith(
      expect.objectContaining({
        id: fixtureProfile.id,
        snapshot: expect.objectContaining({ default_model_tier: "balanced", concurrency_limit: 3 }),
      }),
    );
  });

  it("saveAsNewProfile() creates and activates a new profile from the current draft", async () => {
    const save = vi.fn().mockResolvedValue(true);
    const config = makeConfig({ save });
    const create = vi.fn().mockResolvedValue({ ...fixtureProfile, id: "prf_new" });
    const activate = vi.fn().mockResolvedValue(fixtureConfig);
    const { result } = renderHook(() => useDrift(config, makeProfiles({ create, activate }), []));

    await act(async () => {
      await result.current.saveAsNewProfile("new-profile", "desc");
    });

    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({ name: "new-profile", description: "desc" }),
    );
    expect(activate).toHaveBeenCalledWith("prf_new");
    expect(config.refetch).toHaveBeenCalled();
  });
});
