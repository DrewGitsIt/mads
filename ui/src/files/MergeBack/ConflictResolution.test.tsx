import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ConflictResolution } from "./ConflictResolution";
import { fixtureConflictMarkers } from "../../router/fixtures";

// Same interception as DiffViewer.test.tsx — happy-dom can't run real
// Monaco, and MergeEditor is a seed-only stub anyway (HANDOFF §5).
vi.mock("@monaco-editor/react", () => ({
  default: (props: { value?: string; language?: string }) => (
    <div
      data-testid="fake-editor"
      data-value={props.value}
      data-language={props.language}
    />
  ),
}));

const files = ["pkg/contracts/session.go", "services/data/internal/store/sessions.go"];

describe("ConflictResolution", () => {
  it("renders the conflicted branch and file list", () => {
    render(
      <ConflictResolution
        branch="mads/tsk_retry-backoff"
        files={files}
        onOpenInEditor={vi.fn()}
        onRetry={vi.fn()}
      />,
    );

    expect(screen.getByText("Merge conflicted")).toBeTruthy();
    expect(screen.getByText("mads/tsk_retry-backoff")).toBeTruthy();
    for (const path of files) {
      expect(screen.getByText(path)).toBeTruthy();
    }
  });

  it("disables the external-editor handoff without a worktreeDir, and shows a hint", () => {
    render(<ConflictResolution files={files} onOpenInEditor={vi.fn()} onRetry={vi.fn()} />);

    expect(
      screen.getByRole("button", { name: "Open worktree in external editor" }).hasAttribute(
        "disabled",
      ),
    ).toBe(true);
    expect(screen.getByText(/No worktree path available/)).toBeTruthy();
  });

  it("calls onOpenInEditor with the worktreeDir when available", () => {
    const onOpenInEditor = vi.fn();
    render(
      <ConflictResolution
        files={files}
        worktreeDir="/home/drew/.mads/worktrees/ses_x/tsk_retry-backoff"
        onOpenInEditor={onOpenInEditor}
        onRetry={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open worktree in external editor" }));
    expect(onOpenInEditor).toHaveBeenCalledWith(
      "/home/drew/.mads/worktrees/ses_x/tsk_retry-backoff",
    );
  });

  it("calls onRetry when Retry merge is clicked", () => {
    const onRetry = vi.fn();
    render(<ConflictResolution files={files} onOpenInEditor={vi.fn()} onRetry={onRetry} />);

    fireEvent.click(screen.getByRole("button", { name: "Retry merge" }));
    expect(onRetry).toHaveBeenCalled();
  });

  it("renders the seed-only MergeEditor when a conflictPreview is supplied", async () => {
    render(
      <ConflictResolution
        files={files}
        onOpenInEditor={vi.fn()}
        onRetry={vi.fn()}
        conflictPreview={{ path: files[0], text: fixtureConflictMarkers }}
      />,
    );

    const editor = await screen.findByTestId("fake-editor");
    expect(editor.getAttribute("data-value")).toBe(fixtureConflictMarkers);
  });

  it("omits the MergeEditor when no conflictPreview is supplied", () => {
    render(<ConflictResolution files={files} onOpenInEditor={vi.fn()} onRetry={vi.fn()} />);
    expect(screen.queryByTestId("fake-editor")).toBeNull();
  });
});
