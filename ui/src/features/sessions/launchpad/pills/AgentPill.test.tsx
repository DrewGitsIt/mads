import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AgentPill } from "./AgentPill";
import type { Agent } from "../../../../router/types";

function makeAgent(overrides: Partial<Agent>): Agent {
  return {
    id: "ag1",
    name: "backend-architect",
    prompt: "…",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("AgentPill", () => {
  it("lists fetched agents in the popover and reports the selection", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify([makeAgent({})]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const onChange = vi.fn();

    render(<AgentPill onChange={onChange} onAuto={vi.fn()} />);
    fireEvent.click(screen.getByText("Select agent"));

    await waitFor(() => expect(screen.getByText("backend-architect")).toBeTruthy());
    fireEvent.click(screen.getByText("backend-architect"));

    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ name: "backend-architect" }));
    vi.unstubAllGlobals();
  });

  it("shows the resolved default agent when unset", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(
      <AgentPill
        resolved={{ value: "backend-architect", source: "workspace" }}
        onChange={vi.fn()}
        onAuto={vi.fn()}
      />,
    );
    expect(screen.getByText(/backend-architect \(default\)/)).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("shows the plain placeholder when nothing resolves", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<AgentPill onChange={vi.fn()} onAuto={vi.fn()} />);
    expect(screen.getByText("Select agent")).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("selecting Auto reports onAuto and shows Auto as the trigger label", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const onAuto = vi.fn();
    render(<AgentPill onChange={vi.fn()} onAuto={onAuto} />);
    fireEvent.click(screen.getByText("Select agent"));
    fireEvent.click(screen.getByText("Auto"));
    expect(onAuto).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("shows Auto as the trigger label when value is auto", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<AgentPill value="auto" onChange={vi.fn()} onAuto={vi.fn()} />);
    expect(screen.getByText("Auto")).toBeTruthy();
    vi.unstubAllGlobals();
  });
});
