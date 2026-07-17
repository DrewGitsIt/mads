import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { WorkspacePill } from "./WorkspacePill";
import type { Workspace } from "../../../../router/types";

function makeWorkspace(overrides: Partial<Workspace>): Workspace {
  return {
    id: "ws1",
    name: "mads-core",
    defaults: {},
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("WorkspacePill", () => {
  it("lists fetched workspaces in the popover and reports the selection", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify([makeWorkspace({})]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const onChange = vi.fn();

    render(<WorkspacePill onChange={onChange} />);
    fireEvent.click(screen.getByText("Select workspace"));

    await waitFor(() => expect(screen.getByText("mads-core")).toBeTruthy());
    fireEvent.click(screen.getByText("mads-core"));

    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ name: "mads-core" }));
    vi.unstubAllGlobals();
  });

  it("shows the resolved default workspace when unset", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<WorkspacePill resolved="mads" onChange={vi.fn()} />);
    expect(screen.getByText(/mads \(default\)/)).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("shows the plain placeholder when nothing resolves", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<WorkspacePill onChange={vi.fn()} />);
    expect(screen.getByText("Select workspace")).toBeTruthy();
    vi.unstubAllGlobals();
  });
});
