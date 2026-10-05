import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { PathField } from "./PathField";

function stubPicker(result: string | null) {
  const pickDirectory = vi.fn().mockResolvedValue(result);
  window.mads = { dialog: { pickDirectory } };
  return pickDirectory;
}

describe("PathField", () => {
  afterEach(() => {
    delete window.mads;
  });

  it("is a plain text field outside the desktop app", () => {
    const onChange = vi.fn();
    render(<PathField aria-label="Path" value="" onChange={onChange} />);
    expect(screen.queryByText("Browse…")).toBeNull();
    fireEvent.change(screen.getByLabelText("Path"), { target: { value: "~/repo" } });
    expect(onChange).toHaveBeenCalledWith("~/repo");
  });

  it("fills the field from the native folder chooser, starting at the typed path", async () => {
    const pickDirectory = stubPicker("/home/me/repo/mads");
    const onChange = vi.fn();
    render(<PathField aria-label="Path" value="/home/me" onChange={onChange} dialogTitle="Pick" />);
    fireEvent.click(screen.getByText("Browse…"));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("/home/me/repo/mads"));
    expect(pickDirectory).toHaveBeenCalledWith({ title: "Pick", defaultPath: "/home/me" });
  });

  it("routes a chosen folder to onPick when given, and ignores a cancel", async () => {
    stubPicker(null);
    const onChange = vi.fn();
    const onPick = vi.fn();
    const { rerender } = render(
      <PathField aria-label="Path" value="" onChange={onChange} onPick={onPick} />,
    );
    fireEvent.click(screen.getByText("Browse…"));
    await waitFor(() => expect(window.mads?.dialog?.pickDirectory).toHaveBeenCalled());
    expect(onPick).not.toHaveBeenCalled();

    stubPicker("/srv/x");
    rerender(<PathField aria-label="Path" value="" onChange={onChange} onPick={onPick} />);
    fireEvent.click(screen.getByText("Browse…"));
    await waitFor(() => expect(onPick).toHaveBeenCalledWith("/srv/x"));
    expect(onChange).not.toHaveBeenCalled();
  });
});
