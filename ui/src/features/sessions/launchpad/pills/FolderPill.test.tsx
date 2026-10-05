import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { FolderPill, folderName } from "./FolderPill";
import type { Workspace } from "../../../../router/types";

function makeWorkspace(overrides: Partial<Workspace>): Workspace {
  return {
    id: "ws1",
    name: "mads-core",
    repos: [{ path: "~/repo/mads", primary: true }],
    defaults: {},
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function stubWorkspaces(list: Workspace[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(JSON.stringify(list), { status: 200 })),
  );
}

describe("folderName", () => {
  it("is the last segment, ignoring a trailing slash", () => {
    expect(folderName("/home/me/repo/mads/")).toBe("mads");
    expect(folderName("~")).toBe("~");
  });
});

describe("FolderPill", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    delete window.mads;
  });

  it("takes any typed path on Enter, not just a registered workspace", () => {
    stubWorkspaces([]);
    const onPickDir = vi.fn();
    render(<FolderPill onPickDir={onPickDir} onPickWorkspace={vi.fn()} />);
    fireEvent.click(screen.getByText("Choose folder"));
    const input = screen.getByLabelText("Folder path");
    fireEvent.change(input, { target: { value: "  ~/scratch/thing " } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onPickDir).toHaveBeenCalledWith("~/scratch/thing");
  });

  it("starts in a folder chosen in the native dialog", async () => {
    stubWorkspaces([]);
    window.mads = { dialog: { pickDirectory: vi.fn().mockResolvedValue("/srv/app") } };
    const onPickDir = vi.fn();
    render(<FolderPill onPickDir={onPickDir} onPickWorkspace={vi.fn()} />);
    fireEvent.click(screen.getByText("Choose folder"));
    fireEvent.click(screen.getByText("Browse…"));
    await waitFor(() => expect(onPickDir).toHaveBeenCalledWith("/srv/app"));
  });

  it("offers recent folders and workspaces as shortcuts", async () => {
    stubWorkspaces([makeWorkspace({})]);
    const onPickDir = vi.fn();
    const onPickWorkspace = vi.fn();
    render(
      <FolderPill
        recent={["/home/me/repo/site"]}
        onPickDir={onPickDir}
        onPickWorkspace={onPickWorkspace}
      />,
    );
    fireEvent.click(screen.getByText("Choose folder"));
    fireEvent.click(screen.getByText("site"));
    expect(onPickDir).toHaveBeenCalledWith("/home/me/repo/site");

    fireEvent.click(screen.getByText("Choose folder"));
    await waitFor(() => expect(screen.getByText("mads-core")).toBeTruthy());
    expect(screen.getByText("~/repo/mads")).toBeTruthy();
    fireEvent.click(screen.getByText("mads-core"));
    expect(onPickWorkspace).toHaveBeenCalledWith(expect.objectContaining({ name: "mads-core" }));
  });

  it("names the picked folder, with the full path as its tooltip", () => {
    stubWorkspaces([]);
    render(<FolderPill dir="/home/me/repo/mads" onPickDir={vi.fn()} onPickWorkspace={vi.fn()} />);
    const trigger = screen.getByText("mads").closest("button");
    expect(trigger?.title).toBe("/home/me/repo/mads");
  });

  it("shows where an unstaged session would start", () => {
    stubWorkspaces([]);
    const { rerender } = render(
      <FolderPill
        resolvedDir={{ value: "/home/me/repo/mads", source: "config" }}
        onPickDir={vi.fn()}
        onPickWorkspace={vi.fn()}
      />,
    );
    expect(screen.getByText(/mads \(default\)/)).toBeTruthy();
    rerender(
      <FolderPill
        resolvedWorkspace="core"
        resolvedDir={{ value: "/home/me/repo/mads", source: "workspace" }}
        onPickDir={vi.fn()}
        onPickWorkspace={vi.fn()}
      />,
    );
    expect(screen.getByText(/core \(default\)/)).toBeTruthy();
  });
});
