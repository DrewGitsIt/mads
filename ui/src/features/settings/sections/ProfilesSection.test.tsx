import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProfilesSection } from "./ProfilesSection";
import { fixtureConfig, fixtureProfile } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";
import type { UseProfiles } from "../state/useProfiles";
import type { Profile } from "../../../router/types";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    saveFields: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function makeProfiles(overrides: Partial<UseProfiles> = {}): UseProfiles {
  return {
    profiles: [fixtureProfile],
    loading: false,
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    activate: vi.fn().mockResolvedValue(fixtureConfig),
    ...overrides,
  };
}

describe("ProfilesSection", () => {
  it("shows the ACTIVE badge only on the profile matching config.saved.active_profile_id", () => {
    const secondProfile: Profile = { ...fixtureProfile, id: "prf_other", name: "other-profile" };
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: "prf_focus" } })}
        profiles={makeProfiles({ profiles: [fixtureProfile, secondProfile] })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    const focusCard = screen.getByText("focus-mode").closest("div")!;
    expect(within(focusCard).queryByText("Active")).toBeTruthy();
    const otherCard = screen.getByText("other-profile").closest("div")!;
    expect(within(otherCard).queryByText("Active")).toBeNull();
  });

  it("renders Rename and Delete for an inactive, non-system profile", () => {
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles()}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: /rename/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^delete$/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /switch to this profile/i })).toBeTruthy();
  });

  it("hides Rename/Delete on a system (built-in) profile", () => {
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles({ profiles: [{ ...fixtureProfile, system: true }] })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    expect(screen.queryByRole("button", { name: /rename/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^delete$/i })).toBeNull();
    expect(screen.getByText("Built-in")).toBeTruthy();
  });

  it("Rename → edit name → Save calls profiles.update with the new name", async () => {
    const update = vi.fn().mockResolvedValue(fixtureProfile);
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles({ update })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /rename/i }));
    fireEvent.change(screen.getByLabelText("Profile name"), { target: { value: "renamed" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() =>
      expect(update).toHaveBeenCalledWith(expect.objectContaining({ id: "prf_focus", name: "renamed" })),
    );
  });

  it("Delete → confirm calls profiles.remove", async () => {
    const remove = vi.fn().mockResolvedValue(undefined);
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles({ remove })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }));
    // Confirm appears with its own Delete button below the card action's.
    const deletes = screen.getAllByRole("button", { name: /^delete$/i });
    fireEvent.click(deletes[deletes.length - 1]);

    await waitFor(() => expect(remove).toHaveBeenCalledWith("prf_focus"));
  });

  it("clicking Switch opens the ProfileSwitchDialog", () => {
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles()}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /switch to this profile/i }));
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("applying the switch calls onSwitched with the pre-switch config and closes the dialog", async () => {
    const onSwitched = vi.fn();
    const previous = { ...fixtureConfig, active_profile_id: undefined };
    const activate = vi.fn().mockResolvedValue({ ...fixtureConfig, active_profile_id: "prf_focus" });
    render(
      <ProfilesSection
        config={makeConfig({ saved: previous, refetch: vi.fn().mockResolvedValue(undefined) })}
        profiles={makeProfiles({ activate })}
        credentialNames={[]}
        onSwitched={onSwitched}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /switch to this profile/i }));
    fireEvent.click(screen.getByRole("button", { name: /switch profile/i }));

    // fixtureProfile's snapshot changes concurrency_limit, a restart-required field.
    await waitFor(() => expect(activate).toHaveBeenCalledWith("prf_focus"));
    await waitFor(() => expect(onSwitched).toHaveBeenCalledWith(previous, true));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("cancelling the dialog closes it without switching", () => {
    const activate = vi.fn();
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles({ activate })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /switch to this profile/i }));
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(activate).not.toHaveBeenCalled();
  });

  describe("new profile card", () => {
    it("is the grid's last card, and the only one when there are no profiles", () => {
      render(
        <ProfilesSection
          config={makeConfig()}
          profiles={makeProfiles({ profiles: [] })}
          credentialNames={[]}
          onSwitched={vi.fn()}
        />,
      );
      expect(screen.getByRole("button", { name: /New profile from current settings/ })).toBeTruthy();
      expect(screen.queryByText("No profiles yet.")).toBeNull();
    });

    it("names and creates a profile from the saved settings, then switches to it like Save as new profile", async () => {
      const created: Profile = { ...fixtureProfile, id: "prf_new", name: "Night shift" };
      const create = vi.fn().mockResolvedValue(created);
      const activate = vi.fn().mockResolvedValue(fixtureConfig);
      const saved = { ...fixtureConfig, theme: "light" as const };
      const config = makeConfig({ saved, draft: saved });
      render(
        <ProfilesSection
          config={config}
          profiles={makeProfiles({ create, activate })}
          credentialNames={[]}
          onSwitched={vi.fn()}
        />,
      );

      fireEvent.click(screen.getByRole("button", { name: /New profile from current settings/ }));
      fireEvent.change(screen.getByLabelText("New profile name"), { target: { value: "  Night shift " } });
      fireEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() => expect(config.refetch).toHaveBeenCalled());
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({ name: "Night shift", snapshot: expect.objectContaining({ theme: "light" }) }),
      );
      expect(activate).toHaveBeenCalledWith("prf_new");
      // Back to the plain card.
      await waitFor(() => expect(screen.queryByLabelText("New profile name")).toBeNull());
    });

    it("Cancel puts the card back without creating anything", () => {
      const create = vi.fn();
      render(
        <ProfilesSection config={makeConfig()} profiles={makeProfiles({ create })} credentialNames={[]} onSwitched={vi.fn()} />,
      );
      fireEvent.click(screen.getByRole("button", { name: /New profile from current settings/ }));
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
      expect(screen.getByRole("button", { name: /New profile from current settings/ })).toBeTruthy();
      expect(create).not.toHaveBeenCalled();
    });

    it("waits while there are unsaved changes", () => {
      render(
        <ProfilesSection
          config={makeConfig({ dirtyFields: ["theme"] })}
          profiles={makeProfiles()}
          credentialNames={[]}
          onSwitched={vi.fn()}
        />,
      );
      const card = screen.getByRole("button", { name: /New profile from current settings/ }) as HTMLButtonElement;
      expect(card.disabled).toBe(true);
    });
  });
});
