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
    fireEvent.click(screen.getByText("Select persona"));

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
    expect(screen.getByText("Select persona")).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("selecting Auto reports onAuto and shows Auto as the trigger label", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const onAuto = vi.fn();
    render(<AgentPill onChange={vi.fn()} onAuto={onAuto} />);
    fireEvent.click(screen.getByText("Select persona"));
    fireEvent.click(screen.getByText("Auto"));
    expect(onAuto).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("reads a saved Auto default as Auto, not the raw value", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(
      <AgentPill resolved={{ value: "auto", source: "config" }} onChange={vi.fn()} onAuto={vi.fn()} />,
    );
    expect(screen.getByText("Auto (default)")).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("shows Auto as the trigger label when value is auto", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<AgentPill value="auto" onChange={vi.fn()} onAuto={vi.fn()} />);
    expect(screen.getByText("Auto")).toBeTruthy();
    vi.unstubAllGlobals();
  });
  it("shows the persona name, not its id, once a persona is selected", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(JSON.stringify([makeAgent({ id: "agt_123e4567" })]), { status: 200 }),
        ),
    );
    render(<AgentPill value="agt_123e4567" onChange={vi.fn()} onAuto={vi.fn()} />);
    await waitFor(() => expect(screen.getByText("backend-architect")).toBeTruthy());
    expect(screen.queryByText(/agt_123e4567/)).toBeNull();
    vi.unstubAllGlobals();
  });
});
