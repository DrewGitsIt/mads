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
        knownModelIds={[]}
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
        knownModelIds={[]}
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
        knownModelIds={[]}
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
        knownModelIds={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    const options = screen.getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
    expect(options).toContain("build");
    expect(options).not.toContain("test");
  });

  it("shows the model field only in agent mode", () => {
    const { rerender } = render(
      <StageCard
        stage={{ name: "build", agent: "auto" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    expect(screen.getByText("Model")).toBeTruthy();

    rerender(
      <StageCard
        stage={{ name: "build", command: "make build" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    expect(screen.queryByText("Model")).toBeNull();
  });

  it("trims model input and clears to undefined when blank", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "architect" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );
    const input = screen.getByText("Model").closest("label")!.querySelector("input")!;

    fireEvent.change(input, { target: { value: "  claude-haiku-4-5-20251001  " } });
    expect(onChange).toHaveBeenCalledWith({
      name: "build",
      agent: "architect",
      model: "claude-haiku-4-5-20251001",
    });

    fireEvent.change(input, { target: { value: "   " } });
    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: "architect", model: undefined });
  });

  it("shows the inherited model as placeholder when blank", () => {
    render(
      <StageCard
        stage={{ name: "build", agent: "architect" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        inheritedModel="sonnet"
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    const input = screen.getByText("Model").closest("label")!.querySelector("input")!;
    expect(input.placeholder).toBe("sonnet (inherited)");
  });

  it("populates the model datalist from knownModelIds", () => {
    render(
      <StageCard
        stage={{ name: "build", agent: "architect" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={["claude-sonnet-5", "claude-haiku-4-5-20251001"]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    const options = Array.from(document.querySelectorAll("datalist option")).map(
      (o) => (o as HTMLOptionElement).value,
    );
    expect(options).toEqual(["claude-sonnet-5", "claude-haiku-4-5-20251001"]);
  });
});
