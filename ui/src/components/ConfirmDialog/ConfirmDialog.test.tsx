import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ConfirmDialog } from "./ConfirmDialog";

describe("ConfirmDialog", () => {
  it("renders nothing when closed", () => {
    render(
      <ConfirmDialog
        open={false}
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("renders title, description, and labels when open", () => {
    render(
      <ConfirmDialog
        open
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(screen.getByText("Delete session")).toBeTruthy();
    expect(screen.getByText("This can't be undone.")).toBeTruthy();
    expect(screen.getByText("Delete")).toBeTruthy();
    expect(screen.getByText("Cancel")).toBeTruthy();
  });

  it("calls onConfirm when the confirm button is clicked", () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText("Delete"));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("calls onCancel when the cancel button is clicked", () => {
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={vi.fn()}
        onCancel={onCancel}
      />,
    );
    fireEvent.click(screen.getByText("Cancel"));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("calls onCancel when the backdrop is clicked", () => {
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={vi.fn()}
        onCancel={onCancel}
      />,
    );
    fireEvent.mouseDown(screen.getByRole("alertdialog").parentElement as Element);
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("does not call onCancel when the dialog itself is clicked", () => {
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={vi.fn()}
        onCancel={onCancel}
      />,
    );
    fireEvent.mouseDown(screen.getByRole("alertdialog"));
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("calls onCancel on Escape", () => {
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Delete session"
        description="This can't be undone."
        confirmLabel="Delete"
        onConfirm={vi.fn()}
        onCancel={onCancel}
      />,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });
  it("routes Escape to onDismiss, not the cancel choice, when given", () => {
    const onCancel = vi.fn();
    const onDismiss = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Close it?"
        description="x"
        confirmLabel="Land and close"
        cancelLabel="Keep for later"
        onConfirm={vi.fn()}
        onCancel={onCancel}
        onDismiss={onDismiss}
      />,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onDismiss).toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText("Keep for later"));
    expect(onCancel).toHaveBeenCalled();
  });

  it("can disable the confirm button", () => {
    render(
      <ConfirmDialog
        open
        title="Commit"
        description="x"
        confirmLabel="Commit 0 files"
        confirmTone="primary"
        confirmDisabled
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect((screen.getByText("Commit 0 files") as HTMLButtonElement).disabled).toBe(true);
  });

  it("renders an optional third action between cancel and confirm", () => {
    const onSecondary = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Unsaved changes"
        description="Save them?"
        confirmLabel="Save"
        confirmTone="primary"
        cancelLabel="Keep editing"
        secondaryLabel="Discard"
        onSecondary={onSecondary}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    const labels = screen.getAllByRole("button").map((b) => b.textContent);
    expect(labels).toEqual(["Keep editing", "Discard", "Save"]);
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(onSecondary).toHaveBeenCalled();
  });
});
