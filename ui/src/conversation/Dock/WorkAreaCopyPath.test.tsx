import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { SessionDock } from "./SessionDock";
import { fixtureIsolatedSession } from "../../router/fixtures";
import { resetProviderModelCache } from "../../router/useProviderModels";
import { resetProviderInfoCache } from "../../router/useProviderInfos";
import type { Session } from "../../router/types";

const FOLDER = "/home/user/repo/mads";
const WORKTREE = "/home/user/.mads/session-worktrees/session-isolated";

const inPlace: Session = {
  ...fixtureIsolatedSession,
  worktree_dir: undefined,
  worktree_branch: undefined,
  worktree_base: undefined,
  cwd: FOLDER,
};

function openPopover(session: Session) {
  render(
    <SessionDock
      session={session}
      workspaceName="mads"
      mode="chat"
      onModeChange={vi.fn()}
      work={{ onCommit: vi.fn(), onLand: vi.fn() }}
    />,
  );
  fireEvent.click(screen.getByTestId("dock-location"));
  return screen.getByTestId("work-popover");
}

/** Lets the clipboard write's promise settle. */
const settle = () => act(async () => {});

function stubNavigatorClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText, readText: vi.fn().mockResolvedValue("") },
    configurable: true,
  });
}

describe("dock ⎇ popover: copy a path", () => {
  beforeEach(() => {
    resetProviderModelCache();
    resetProviderInfoCache();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    delete (window as { mads?: unknown }).mads;
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
  });

  it("copies the full worktree path through the Electron clipboard bridge, then says Copied", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    (window as { mads?: unknown }).mads = { clipboard: { writeText, readText: vi.fn() } };
    const navWrite = vi.fn().mockResolvedValue(undefined);
    stubNavigatorClipboard(navWrite);

    const pop = openPopover(fixtureIsolatedSession);
    fireEvent.click(within(pop).getByRole("button", { name: "Copy worktree path" }));
    await settle();

    expect(writeText).toHaveBeenCalledWith(WORKTREE);
    expect(navWrite).not.toHaveBeenCalled();
    const done = within(pop).getByRole("button", { name: "Copied worktree path" });
    expect(done.textContent).toBe("Copied");
    // Copying doesn't close the popover.
    expect(screen.getByTestId("work-popover")).toBeTruthy();
  });

  it("marks the worktree, not the folder, as where an isolated session works", () => {
    const pop = openPopover(fixtureIsolatedSession);
    expect(within(pop).getByTestId("work-path-worktree").hasAttribute("data-workdir")).toBe(true);
    expect(within(pop).getByTestId("work-path-folder").hasAttribute("data-workdir")).toBe(false);
    // The path itself is plain, selectable text.
    expect(within(pop).getByTestId("work-path-worktree").textContent).toContain(WORKTREE);
  });

  it("copies the folder an in-place session works in, falling back to navigator.clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubNavigatorClipboard(writeText);

    const pop = openPopover(inPlace);
    expect(within(pop).queryByRole("button", { name: "Copy worktree path" })).toBeNull();
    expect(within(pop).getByTestId("work-path-folder").hasAttribute("data-workdir")).toBe(true);
    fireEvent.click(within(pop).getByRole("button", { name: "Copy folder path" }));
    await settle();

    expect(writeText).toHaveBeenCalledWith(FOLDER);
    expect(within(pop).getByRole("button", { name: "Copied folder path" })).toBeTruthy();
  });

  it("goes back to Copy after a moment", async () => {
    vi.useFakeTimers();
    stubNavigatorClipboard(vi.fn().mockResolvedValue(undefined));
    const pop = openPopover(fixtureIsolatedSession);
    fireEvent.click(within(pop).getByRole("button", { name: "Copy folder path" }));
    await settle();
    expect(within(pop).getByRole("button", { name: "Copied folder path" })).toBeTruthy();

    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(within(pop).getByRole("button", { name: "Copy folder path" }).textContent).toBe("Copy");
  });

  it("doesn't claim Copied when the write fails", async () => {
    stubNavigatorClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    const pop = openPopover(fixtureIsolatedSession);
    fireEvent.click(within(pop).getByRole("button", { name: "Copy worktree path" }));
    await settle();
    expect(within(pop).queryByRole("button", { name: "Copied worktree path" })).toBeNull();
  });
});
