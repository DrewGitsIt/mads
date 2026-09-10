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

    const loopBackTrigger = screen.getByText("Loop back to").closest("label")!.querySelector("button")!;
    fireEvent.click(loopBackTrigger);
    const options = screen.getAllByRole("option").map((o) => o.textContent);
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

  it("shows model suggestions from knownModelIds on focus", () => {
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
    const input = screen.getByText("Model").closest("label")!.querySelector("input")!;
    fireEvent.focus(input);
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["claude-sonnet-5", "claude-haiku-4-5-20251001"]);
  });

  it("still accepts a model id that isn't in knownModelIds (provider is the authority)", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "architect" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={["claude-sonnet-5"]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );
    const input = screen.getByText("Model").closest("label")!.querySelector("input")!;
    fireEvent.change(input, { target: { value: "some-future-model" } });
    expect(onChange).toHaveBeenCalledWith({
      name: "build",
      agent: "architect",
      model: "some-future-model",
    });
  });

  it("edits the timeout field", () => {
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

    fireEvent.change(screen.getByText("Timeout").closest("label")!.querySelector("input")!, {
      target: { value: "15m" },
    });

    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: "auto", timeout: "15m" });
  });

  it("clears timeout to undefined when blanked", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "auto", timeout: "15m" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Timeout").closest("label")!.querySelector("input")!, {
      target: { value: "" },
    });

    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: "auto", timeout: undefined });
  });

  it("renders a timeout field error", () => {
    render(
      <StageCard
        stage={{ name: "build", agent: "auto", timeout: "not-a-duration" }}
        index={0}
        earlierStageNames={[]}
        errors={[{ field: "timeout", message: "invalid duration" }]}
        knownModelIds={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    expect(screen.getByText("invalid duration")).toBeTruthy();
  });

  it("edits retry.max, defaulting to open when a retry block is already present", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "auto", retry: { max: 2 } }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );

    const maxInput = screen.getByText("Max retries").closest("label")!.querySelector("input")!;
    expect((maxInput as HTMLInputElement).value).toBe("2");

    fireEvent.change(maxInput, { target: { value: "0" } });
    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: "auto", retry: { max: 0 } });
  });

  it("selects retry.when from the three valid values", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "auto", retry: {} }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );

    const trigger = screen.getByText("Retry on").closest("label")!.querySelector("button")!;
    fireEvent.click(trigger);
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["—", "Error", "Always", "Never"]);

    fireEvent.click(screen.getByRole("option", { name: "Always" }));
    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: "auto", retry: { when: "always" } });
  });

  it("edits retry.backoff", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "auto", retry: {} }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={[]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Backoff").closest("label")!.querySelector("input")!, {
      target: { value: "5s" },
    });
    expect(onChange).toHaveBeenCalledWith({ name: "build", agent: "auto", retry: { backoff: "5s" } });
  });

  it("routes retry.max/when/backoff errors to the retry block", () => {
    render(
      <StageCard
        stage={{ name: "build", agent: "auto", retry: { max: 20 } }}
        index={0}
        earlierStageNames={[]}
        errors={[
          { field: "retry.max", message: "must be between 0 and 10" },
          { field: "retry.when", message: "unknown value" },
          { field: "retry.backoff", message: "invalid duration" },
        ]}
        knownModelIds={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    expect(screen.getByText("must be between 0 and 10")).toBeTruthy();
    expect(screen.getByText("unknown value")).toBeTruthy();
    expect(screen.getByText("invalid duration")).toBeTruthy();
  });

  it("routes a whole-block retry error (e.g. rejected on a merge stage)", () => {
    render(
      <StageCard
        stage={{ name: "merge", command: "git merge main", retry: { max: 1 } }}
        index={0}
        earlierStageNames={[]}
        errors={[{ field: "retry", message: "retry is not allowed on a merge stage" }]}
        knownModelIds={[]}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    expect(screen.getByText("retry is not allowed on a merge stage")).toBeTruthy();
  });

  it("navigates model suggestions with the keyboard and selects on Enter", () => {
    const onChange = vi.fn();
    render(
      <StageCard
        stage={{ name: "build", agent: "architect" }}
        index={0}
        earlierStageNames={[]}
        errors={[]}
        knownModelIds={["claude-sonnet-5", "claude-haiku-4-5-20251001"]}
        onChange={onChange}
        onRemove={vi.fn()}
      />,
    );
    const input = screen.getByText("Model").closest("label")!.querySelector("input")!;
    fireEvent.focus(input);
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith({
      name: "build",
      agent: "architect",
      model: "claude-haiku-4-5-20251001",
    });
  });
});
