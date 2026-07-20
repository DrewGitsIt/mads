import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProfileSwitchDialog } from "./ProfileSwitchDialog";
import { fixtureConfig, fixtureProfile } from "../../router/fixtures";

describe("ProfileSwitchDialog", () => {
  it("lists exactly the changed fields, grouped by section, with a restart badge", () => {
    render(
      <ProfileSwitchDialog
        profile={fixtureProfile}
        currentConfig={fixtureConfig}
        credentialNames={[]}
        activate={vi.fn()}
        refetchConfig={vi.fn()}
        onApplied={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("General")).toBeTruthy();
    expect(screen.getByText("Pipeline & synthesis")).toBeTruthy();
    expect(screen.getByText("Concurrency limit")).toBeTruthy();
    expect(screen.getByText("3 → 1")).toBeTruthy();
    expect(screen.getByText("Default model tier")).toBeTruthy();
    expect(screen.getByText("balanced → cheapest")).toBeTruthy();
    expect(screen.getAllByText("Restart")).toHaveLength(1);
    expect(screen.getByText("Some changes apply on mads restart")).toBeTruthy();
  });

  it("shows credential changes by name with active/inactive wording", () => {
    // currentConfig explicitly binds only anthropic, so openai starts
    // inactive — isCredentialActive treats undefined/empty bindings as
    // "everything active," which would mask the inactive -> active case.
    const currentWithBindings = { ...fixtureConfig, credential_bindings: ["anthropic"] };
    const profileWithBindings = {
      ...fixtureProfile,
      snapshot: { ...fixtureProfile.snapshot, credential_bindings: ["openai"] },
    };
    render(
      <ProfileSwitchDialog
        profile={profileWithBindings}
        currentConfig={currentWithBindings}
        credentialNames={["anthropic", "openai"]}
        activate={vi.fn()}
        refetchConfig={vi.fn()}
        onApplied={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.getByText("active → inactive")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
    expect(screen.getByText("inactive → active")).toBeTruthy();
  });

  it("says nothing differs when the current config already matches the snapshot", () => {
    render(
      <ProfileSwitchDialog
        profile={{ ...fixtureProfile, snapshot: {} }}
        currentConfig={fixtureConfig}
        credentialNames={[]}
        activate={vi.fn()}
        refetchConfig={vi.fn()}
        onApplied={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText(/No settings differ/)).toBeTruthy();
  });

  it("Apply activates the profile, refetches config, and reports restart-required", async () => {
    const activate = vi.fn().mockResolvedValue(fixtureConfig);
    const refetchConfig = vi.fn().mockResolvedValue(undefined);
    const onApplied = vi.fn();
    render(
      <ProfileSwitchDialog
        profile={fixtureProfile}
        currentConfig={fixtureConfig}
        credentialNames={[]}
        activate={activate}
        refetchConfig={refetchConfig}
        onApplied={onApplied}
        onClose={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText("Switch profile"));
    await waitFor(() => expect(onApplied).toHaveBeenCalledWith(true));
    expect(activate).toHaveBeenCalledWith(fixtureProfile.id);
    expect(refetchConfig).toHaveBeenCalled();
  });

  it("Cancel and Escape both close without switching", () => {
    const onClose = vi.fn();
    const activate = vi.fn();
    render(
      <ProfileSwitchDialog
        profile={fixtureProfile}
        currentConfig={fixtureConfig}
        credentialNames={[]}
        activate={activate}
        refetchConfig={vi.fn()}
        onApplied={vi.fn()}
        onClose={onClose}
      />,
    );
    fireEvent.click(screen.getByText("Cancel"));
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(activate).not.toHaveBeenCalled();
  });
});
