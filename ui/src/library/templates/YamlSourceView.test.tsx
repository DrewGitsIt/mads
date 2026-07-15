import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { YamlSourceView } from "./YamlSourceView";

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: { value: string; language?: string; onChange?: (value: string) => void }) => (
    <textarea
      data-testid="fake-yaml-editor"
      data-language={props.language}
      value={props.value}
      onChange={(e) => props.onChange?.(e.target.value)}
    />
  ),
}));

describe("YamlSourceView", () => {
  it("binds the editor's value directly to source", async () => {
    render(<YamlSourceView source="name: deploy" onChange={vi.fn()} />);
    const editor = await screen.findByTestId("fake-yaml-editor");
    expect(editor.getAttribute("data-language")).toBe("yaml");
    expect((editor as HTMLTextAreaElement).value).toBe("name: deploy");
  });

  it("calls onChange with the edited source", async () => {
    const onChange = vi.fn();
    render(<YamlSourceView source="name: deploy" onChange={onChange} />);
    const editor = await screen.findByTestId("fake-yaml-editor");
    fireEvent.change(editor, { target: { value: "name: deploy2" } });
    expect(onChange).toHaveBeenCalledWith("name: deploy2");
  });
});
