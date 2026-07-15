import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { StageCard } from "./StageCard";
import type { TemplateStage } from "../../router/types";

describe("StageCard", () => {
  it("edits the name field", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "auto" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "compile" },
    });

    expect(onChange).toHaveBeenCalledWith({ name: "compile", agent: "auto" });
  });

  it("switching to command mode clears agent and vice versa", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "auto" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("Command"));

    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: undefined, command: "" });
  });

  it("renders field-level errors routed to this stage", () => {
    render(
      <StageCard
        stage={{ name: "build", agent: "nope" }}
        index={1}
        earlierStageNames={["build"]}
        errors={[{ field: "agent", message: "unknown agent" }]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    expect(screen.getByText("unknown agent")).toBeTruthy();
  });

  it("constrains loop_back options to earlier stages only", () => {
    const stage: TemplateStage = { name: "test", command: "make test", on_fail: {} };
    render(
      <StageCard
        stage={stage}
        index={1}
        earlierStageNames={["build"]}
        errors={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    const options = screen.getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
    expect(options).toContain("build");
    expect(options).not.toContain("test");
  });
});
