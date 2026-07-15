import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { VisualStageList } from "./VisualStageList";
import type { ParsedTemplate } from "../../router/types";

describe("VisualStageList", () => {
  it("renders a StageCard per stage and adds a new one", () => {
    const onChange = vi.fn();
    const parsed: ParsedTemplate = {
      name: "deploy",
      stages: [{ name: "build", command: "make build" }],
    };
    render(<VisualStageList parsed={parsed} parseError={null} errors={[]} onChange={onChange} />);

    expect(screen.getByText("Stage 1")).toBeTruthy();
    fireEvent.click(screen.getByText("Add stage"));

    expect(onChange).toHaveBeenCalledWith({
      name: "deploy",
      stages: [{ name: "build", command: "make build" }, { name: "", agent: "auto" }],
    });
  });

  it("shows a can't-render fallback on a YAML parse error, without throwing", () => {
    render(
      <VisualStageList parsed={null} parseError="bad indentation" errors={[]} onChange={vi.fn()} />,
    );

    expect(screen.getByText("Can't render — fix YAML syntax first.")).toBeTruthy();
    expect(screen.getByText("bad indentation")).toBeTruthy();
  });

  it("routes stages[N].field errors onto the matching StageCard and top-level errors above the list", () => {
    const parsed: ParsedTemplate = {
      name: "",
      stages: [
        { name: "build", command: "make build" },
        { name: "build", agent: "nope" },
      ],
    };
    render(
      <VisualStageList
        parsed={parsed}
        parseError={null}
        errors={[
          { field: "name", message: "required" },
          { field: "stages[1].name", message: 'duplicate stage name "build" (also stages[0])' },
          { field: "stages[1].agent", message: "unknown agent" },
        ]}
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByText("name: required")).toBeTruthy();
    expect(screen.getByText('duplicate stage name "build" (also stages[0])')).toBeTruthy();
    expect(screen.getByText("unknown agent")).toBeTruthy();
  });
});
