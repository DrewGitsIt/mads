import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DiffViewer } from "./DiffViewer";
import { fixtureFileDiffs } from "../../router/fixtures";

// Intercepts the dynamic import DiffViewer.tsx uses for React.lazy — Monaco
// needs Worker/canvas-measurement APIs happy-dom doesn't provide, so real
// Monaco never mounts in this suite (HANDOFF §5).
vi.mock("@monaco-editor/react", () => ({
  DiffEditor: (props: {
    original: string;
    modified: string;
    language?: string;
    height?: string;
    options?: { renderSideBySide?: boolean; automaticLayout?: boolean };
  }) => (
    <div
      data-testid="fake-diff-editor"
      data-original={props.original}
      data-modified={props.modified}
      data-language={props.language}
      data-height={props.height}
      data-side-by-side={String(props.options?.renderSideBySide)}
      data-automatic-layout={String(props.options?.automaticLayout)}
    />
  ),
}));

const diff = fixtureFileDiffs["services/data/internal/ingest.go"];

describe("DiffViewer", () => {
  it("shows a prompt when no file is selected", () => {
    render(<DiffViewer diff={null} onOpenInEditor={vi.fn()} />);
    expect(screen.getByText(/select a file/i)).toBeTruthy();
  });

  it("renders a seeded diff's original/modified/language into the editor, side-by-side by default", async () => {
    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-original")).toBe(diff.original);
    expect(editor.getAttribute("data-modified")).toBe(diff.modified);
    expect(editor.getAttribute("data-language")).toBe("go");
    expect(editor.getAttribute("data-side-by-side")).toBe("true");
  });

  it("toggling to inline flips renderSideBySide to false", async () => {
    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);
    await screen.findByTestId("fake-diff-editor");

    fireEvent.click(screen.getByText("Inline"));

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-side-by-side")).toBe("false");
  });

  it("shows the error state, but keeps the always-visible Open in editor button enabled", () => {
    render(
      <DiffViewer path="a.go" diff={null} error="diff unavailable" onOpenInEditor={vi.fn()} />,
    );
    expect(screen.getByText("diff unavailable")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open in editor" }).hasAttribute("disabled")).toBe(
      false,
    );
  });

  it("disables Open in editor when there is no path at all", () => {
    render(<DiffViewer diff={null} onOpenInEditor={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Open in editor" }).hasAttribute("disabled")).toBe(
      true,
    );
  });

  it("calls onOpenInEditor with the current path", () => {
    const onOpenInEditor = vi.fn();
    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={onOpenInEditor} />);
    fireEvent.click(screen.getByText("Open in editor"));
    expect(onOpenInEditor).toHaveBeenCalledWith(diff.path);
  });

  it("fills the pane instead of a fixed height, with automaticLayout for resizes", async () => {
    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-height")).toBe("100%");
    expect(editor.getAttribute("data-automatic-layout")).toBe("true");
  });

  it("labels the toolbar with the display path, keeping the full path in title", () => {
    render(
      <DiffViewer
        path="/wt/services/data/internal/ingest.go"
        pathLabel="services/data/internal/ingest.go"
        diff={diff}
        onOpenInEditor={vi.fn()}
      />,
    );

    const label = screen.getByText("services/data/internal/ingest.go");
    expect(label.closest("[title]")?.getAttribute("title")).toBe(
      "/wt/services/data/internal/ingest.go",
    );
  });

  it("falls back to the raw path in the toolbar when no pathLabel is given", () => {
    render(<DiffViewer path={diff.path} diff={null} onOpenInEditor={vi.fn()} />);
    expect(screen.getByTitle(diff.path).textContent).toBe(diff.path);
  });

  it("shows no path label when nothing is selected", () => {
    render(<DiffViewer diff={null} onOpenInEditor={vi.fn()} />);
    expect(screen.queryByTitle(/./)).toBeNull();
  });

  it("shows the commit label when commitHash is passed", () => {
    render(
      <DiffViewer path={diff.path} commitHash="a1b2c3d4e5f6" diff={diff} onOpenInEditor={vi.fn()} />,
    );
    expect(screen.getByText("Diff from commit a1b2c3d")).toBeTruthy();
  });

  it("omits the commit label when commitHash is absent", () => {
    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);
    expect(screen.queryByText(/diff from commit/i)).toBeNull();
  });
});
