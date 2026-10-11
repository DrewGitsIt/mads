import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { EditorSection } from "./EditorSection";
import { fixtureConfig } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";

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

describe("EditorSection", () => {
  it("edits the open-files command into the draft", () => {
    const config = makeConfig();
    render(<EditorSection config={config} />);
    const field = screen.getByLabelText("Open files with") as HTMLInputElement;
    expect(field.placeholder).toBe("code -g");
    expect(screen.getByText("The command mads runs to open a file. Leave empty to use $EDITOR.")).toBeTruthy();
    fireEvent.change(field, { target: { value: "zed" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ editor_command: "zed" }));
  });

  it("keybindings are a closed choice with readable labels, saved with the page", () => {
    const config = makeConfig({ draft: { ...fixtureConfig, keybindings: "default" } });
    render(<EditorSection config={config} />);
    const keys = screen.getByRole("radiogroup", { name: "Keybindings" });
    expect(within(keys).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Default", "Vim", "Emacs"]);
    fireEvent.click(within(keys).getByRole("radio", { name: "Vim" }));
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ keybindings: "vim" }));
    expect(config.saveFields).not.toHaveBeenCalled();
  });
});
