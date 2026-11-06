import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { FolderPill, folderName } from "./FolderPill";
import type { Workspace } from "../../../router/types";

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

describe("FolderPill isolation", () => {
  afterEach(() => vi.unstubAllGlobals());

  const base = { onPickDir: vi.fn(), onPickWorkspace: vi.fn() };

  it("shows the default folder's isolation at rest, without opening the menu", () => {
    stubWorkspaces([]);
    render(
      <FolderPill
        {...base}
        resolvedDir={{ value: "/repo/mads", source: "config" }}
        resolvedIsolation={{ value: "worktree", source: "builtin" }}
        dirStatus="git"
        onIsolationChange={vi.fn()}
      />,
    );
    const pill = screen.getByRole("button", { name: /folder mads \(default\)/ });
    expect(screen.getByTestId("folder-isolation").textContent).toBe("⎇ worktree");
    // Nothing staged: no accent dot.
    expect(pill.className).not.toMatch(/modified/);
  });

  it("flips isolation from the menu's switch and marks the pill as changed", () => {
    stubWorkspaces([]);
    const onIsolationChange = vi.fn();
    const { rerender } = render(
      <FolderPill
        {...base}
        resolvedDir={{ value: "/repo/mads", source: "config" }}
        resolvedIsolation={{ value: "worktree", source: "builtin" }}
        dirStatus="git"
        onIsolationChange={onIsolationChange}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /folder mads/ }));
    const toggle = screen.getByRole("switch", { name: /Work in a new worktree/ });
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    fireEvent.click(toggle);
    expect(onIsolationChange).toHaveBeenCalledWith("current_dir");
    // The menu stays open for a switch flip.
    expect(screen.getByRole("switch")).toBeTruthy();

    rerender(
      <FolderPill
        {...base}
        resolvedDir={{ value: "/repo/mads", source: "config" }}
        resolvedIsolation={{ value: "worktree", source: "builtin" }}
        isolation="current_dir"
        dirStatus="git"
        onIsolationChange={onIsolationChange}
      />,
    );
    expect(screen.getByTestId("folder-isolation").textContent).toBe("▭ in place");
    expect(screen.getByRole("button", { name: /folder mads/ }).className).toMatch(/modified/);
  });

  it("forces in place for a plain folder, with the reason on hover and the switch disabled", () => {
    stubWorkspaces([]);
    render(
      <FolderPill
        {...base}
        resolvedDir={{ value: "/home/me/notes", source: "config" }}
        resolvedIsolation={{ value: "worktree", source: "config" }}
        dirStatus="plain"
        onIsolationChange={vi.fn()}
      />,
    );
    const part = screen.getByTestId("folder-isolation");
    expect(part.textContent).toBe("▭ in place");
    expect(part.getAttribute("title")).toBe(
      "notes isn't a git repository, so there's no worktree to isolate this session in.",
    );
    fireEvent.click(screen.getByRole("button", { name: /folder notes/ }));
    const toggle = screen.getByRole("switch") as HTMLButtonElement;
    expect(toggle.disabled).toBe(true);
    expect(toggle.getAttribute("aria-checked")).toBe("false");
  });

  it("offers no isolation for a missing folder", () => {
    stubWorkspaces([]);
    render(
      <FolderPill
        {...base}
        resolvedDir={{ value: "/gone", source: "config" }}
        resolvedIsolation={{ value: "worktree", source: "config" }}
        dirStatus="missing"
        onIsolationChange={vi.fn()}
      />,
    );
    expect(screen.getByTestId("folder-isolation").textContent).toBe("—");
    fireEvent.click(screen.getByRole("button", { name: /folder gone/ }));
    expect(screen.queryByRole("switch")).toBeNull();
  });

  it("is folder-only without onIsolationChange", () => {
    stubWorkspaces([]);
    render(<FolderPill {...base} dir="/repo/mads" />);
    expect(screen.queryByTestId("folder-isolation")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /folder mads/ }));
    expect(screen.queryByRole("switch")).toBeNull();
  });

  it("does not list a workspace's repos while the multiRepo flag is off", () => {
    stubWorkspaces([]);
    render(
      <FolderPill
        {...base}
        dir="/repo/mads"
        isolation="worktree"
        dirStatus="git"
        onIsolationChange={vi.fn()}
        repos={[{ path: "mads-core", primary: true }, { path: "mads-ui" }]}
      />,
    );
    expect(screen.getByTestId("folder-isolation").textContent).toBe("⎇ worktree");
    fireEvent.click(screen.getByRole("button", { name: /folder mads/ }));
    expect(screen.queryByText("mads-ui")).toBeNull();
  });
});
