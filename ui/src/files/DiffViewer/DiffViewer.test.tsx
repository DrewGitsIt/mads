import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DiffViewer, SIDE_BY_SIDE_KEY } from "./DiffViewer";
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
      data-inline-when-limited={String((props.options as { useInlineViewWhenSpaceIsLimited?: boolean })?.useInlineViewWhenSpaceIsLimited)}
      data-word-wrap={(props.options as { wordWrap?: string })?.wordWrap}
    />
  ),
  Editor: (props: { value: string; language?: string }) => (
    <div data-testid="fake-editor" data-value={props.value} data-language={props.language} />
  ),
}));

const diff = fixtureFileDiffs["services/data/internal/ingest.go"];

describe("DiffViewer", () => {
  beforeEach(() => localStorage.clear());

  it("shows a prompt when no file is selected", () => {
    render(<DiffViewer diff={null} onOpenInEditor={vi.fn()} />);
    expect(screen.getByText(/select a file/i)).toBeTruthy();
  });

  it("renders a seeded diff's original/modified/language into the editor, inline by default", async () => {
    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-original")).toBe(diff.original);
    expect(editor.getAttribute("data-modified")).toBe(diff.modified);
    expect(editor.getAttribute("data-language")).toBe("go");
    expect(editor.getAttribute("data-side-by-side")).toBe("false");
    expect(editor.getAttribute("data-word-wrap")).toBe("on");
  });

  it("Side by side is honoured at any width, and remembered", async () => {
    const { unmount } = render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);
    await screen.findByTestId("fake-diff-editor");

    fireEvent.click(screen.getByText("Side by side"));

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-side-by-side")).toBe("true");
    // Monaco would otherwise force inline under 900px, ignoring the toggle.
    expect(editor.getAttribute("data-inline-when-limited")).toBe("false");
    expect(localStorage.getItem(SIDE_BY_SIDE_KEY)).toBe("1");
    unmount();

    render(<DiffViewer path={diff.path} diff={diff} onOpenInEditor={vi.fn()} />);
    expect((await screen.findByTestId("fake-diff-editor")).getAttribute("data-side-by-side")).toBe("true");
    fireEvent.click(screen.getByText("Inline"));
    expect((await screen.findByTestId("fake-diff-editor")).getAttribute("data-side-by-side")).toBe("false");
  });

  it("shows an added file as plain code, without the view toggle", async () => {
    render(
      <DiffViewer path="a.go" diff={{ path: "a.go", original: "", modified: "package a\n\nfunc A() {}\n" }} onOpenInEditor={vi.fn()} />,
    );
    const editor = await screen.findByTestId("fake-editor");
    expect(editor.getAttribute("data-value")).toBe("package a\n\nfunc A() {}\n");
    expect(screen.getByText("New file · 3 lines")).toBeTruthy();
    expect(screen.queryByText("Side by side")).toBeNull();
    // The diff editor stays mounted, hidden and emptied (see DiffBody).
    const hiddenDiff = screen.getByTestId("fake-diff-editor");
    expect(hiddenDiff.closest("[hidden]")).toBeTruthy();
    expect(hiddenDiff.getAttribute("data-modified")).toBe("");
  });

  it("shows a deleted file as plain code of what was removed", async () => {
    render(<DiffViewer path="a.go" diff={{ path: "a.go", original: "x\ny", modified: "" }} onOpenInEditor={vi.fn()} />);
    expect((await screen.findByTestId("fake-editor")).getAttribute("data-value")).toBe("x\ny");
    expect(screen.getByText("Deleted file · 2 lines")).toBeTruthy();
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
