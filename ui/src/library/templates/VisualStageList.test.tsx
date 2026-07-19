import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { VisualStageList } from "./VisualStageList";
import type { ParsedTemplate } from "../../router/types";
import { FakeEventSource } from "../../router/testFakeEventSource";

function stubEmptyFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (url.includes("/agents")) return Promise.resolve(new Response("[]", { status: 200 }));
      if (url.includes("/providers")) return Promise.resolve(new Response("[]", { status: 200 }));
      return Promise.resolve(new Response("{}", { status: 200 }));
    }),
  );
}

describe("VisualStageList", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    stubEmptyFetch();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

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

  it("passes known model ids and the resolved agent's inherited model down to each StageCard", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/providers/fake/models")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({ models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5" }] }),
              { status: 200 },
            ),
          );
        }
        if (url.includes("/providers")) {
          return Promise.resolve(new Response(JSON.stringify([{ name: "fake" }]), { status: 200 }));
        }
        if (url.includes("/agents")) {
          return Promise.resolve(
            new Response(
              JSON.stringify([
                {
                  id: "a1",
                  name: "executor",
                  prompt: "p",
                  created_at: "t",
                  updated_at: "t",
                  preferences: { model: "sonnet" },
                },
              ]),
              { status: 200 },
            ),
          );
        }
        return Promise.resolve(new Response("{}", { status: 200 }));
      }),
    );
    const parsed: ParsedTemplate = {
      name: "deploy",
      stages: [{ name: "executor", agent: "auto" }],
    };
    render(<VisualStageList parsed={parsed} parseError={null} errors={[]} onChange={vi.fn()} />);

    await waitFor(() => {
      const input = screen.getByText("Model").closest("label")!.querySelector("input")!;
      expect(input.placeholder).toBe("sonnet (inherited)");
    });
    const input = screen.getByText("Model").closest("label")!.querySelector("input")!;
    fireEvent.focus(input);
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["claude-sonnet-5"]);
  });
});
