import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TemplateEditor } from "./TemplateEditor";
import type { Template } from "../../router/types";

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: { value: string; onChange?: (value: string) => void }) => (
    <textarea
      data-testid="fake-yaml-editor"
      value={props.value}
      onChange={(e) => props.onChange?.(e.target.value)}
    />
  ),
}));

describe("TemplateEditor", () => {
  it("an edit in visual mode is reflected in the YAML view's source", async () => {
    render(
      <TemplateEditor
        template={null}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("Add stage"));
    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "build" },
    });

    fireEvent.click(screen.getByText("YAML"));

    const editor = (await screen.findByTestId("fake-yaml-editor")) as HTMLTextAreaElement;
    expect(editor.value).toContain("build");
  });

  it("an edit in YAML mode is reflected back in the visual view", async () => {
    render(
      <TemplateEditor
        template={null}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("YAML"));
    const editor = (await screen.findByTestId("fake-yaml-editor")) as HTMLTextAreaElement;
    fireEvent.change(editor, {
      target: { value: "name: deploy\nstages:\n  - name: compile\n    command: make\n" },
    });

    fireEvent.click(screen.getByText("Visual"));

    expect(screen.getByText("Stage 1")).toBeTruthy();
    expect(screen.getByDisplayValue("compile")).toBeTruthy();
  });

  it("a can't-render fallback appears in visual mode on invalid YAML, without crashing the toggle", async () => {
    render(
      <TemplateEditor
        template={null}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("YAML"));
    const editor = (await screen.findByTestId("fake-yaml-editor")) as HTMLTextAreaElement;
    fireEvent.change(editor, { target: { value: "name: [unterminated" } });

    fireEvent.click(screen.getByText("Visual"));

    expect(screen.getByText("Can't render — fix YAML syntax first.")).toBeTruthy();
    fireEvent.click(screen.getByText("YAML"));
    expect(await screen.findByTestId("fake-yaml-editor")).toBeTruthy();
  });

  it("does not show a Delete button for system templates", () => {
    const template: Template = {
      id: "t1",
      name: "deploy",
      source: "name: deploy\nstages: []\n",
      system: true,
      created_at: "t",
      updated_at: "t",
    };
    render(
      <TemplateEditor
        template={template}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    expect(screen.queryByText("Delete")).toBeNull();
  });
});
