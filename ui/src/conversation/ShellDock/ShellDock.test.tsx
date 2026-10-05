import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ShellDock } from "./ShellDock";
import { ShellBridge } from "./shellBridge";
import { makeFakeShellHost } from "./fakeShellHost";

// Typed output reaches the pty through the bridge; these tests need only the
// dock's own behaviour, so the host is the in-memory fake.
function mountDock(overrides: Partial<React.ComponentProps<typeof ShellDock>> = {}) {
  const fake = makeFakeShellHost();
  const bridge = new ShellBridge(fake.host, "ses_1");
  const utils = render(<ShellDock bridge={bridge} {...overrides} />);
  return { fake, bridge, ...utils };
}

describe("ShellDock", () => {
  beforeEach(() => vi.useRealTimers());
  afterEach(() => vi.restoreAllMocks());

  it("mounts a resizable panel and opens its session's shell via the bridge", async () => {
    const { fake, container } = mountDock();

    await waitFor(() => expect(fake.host.open).toHaveBeenCalledTimes(1));
    expect(container.querySelector('[data-testid="resize-handle"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy();
  });

  it("passes cwd through to open", async () => {
    const { fake } = mountDock({ cwd: "/repo/worktree" });

    await waitFor(() =>
      expect(fake.host.open).toHaveBeenCalledWith(
        "ses_1",
        expect.objectContaining({ cwd: "/repo/worktree" }),
      ),
    );
  });

  it("starts at the stored height and reports a resize when the drag ends", async () => {
    const onHeightChange = vi.fn();
    const { container } = mountDock({ height: 350, onHeightChange });

    const panel = container.querySelector('[data-testid="resize-handle"]')!.parentElement!;
    expect(panel.style.height).toBe("350px");

    fireEvent.mouseDown(screen.getByTestId("resize-handle"), { clientY: 500 });
    fireEvent.mouseMove(window, { clientY: 450 });
    fireEvent.mouseUp(window);
    expect(onHeightChange).toHaveBeenCalledWith(400);
  });

  it("the chevron hides and × ends, each without doing the other", async () => {
    const onHide = vi.fn();
    const onEnd = vi.fn();
    mountDock({ onHide, onEnd });

    fireEvent.click(screen.getByRole("button", { name: "Hide shell" }));
    expect(onHide).toHaveBeenCalledTimes(1);
    expect(onEnd).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "End shell" }));
    expect(onEnd).toHaveBeenCalledTimes(1);
  });

  it("unmounting detaches and never ends the shell", async () => {
    const { fake, unmount } = mountDock();
    await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
    await waitFor(() => expect(fake.host.onData).toHaveBeenCalled());

    unmount();

    expect(fake.host.detach).toHaveBeenCalledWith("shell-ses_1");
    expect(fake.host.end).not.toHaveBeenCalled();
  });

  it("a shell that exits shows Start new shell, which opens a fresh one", async () => {
    const { fake } = mountDock();
    await waitFor(() => expect(fake.host.onExit).toHaveBeenCalled());

    act(() => fake.emitExit("shell-ses_1", 0));
    fireEvent.click(await screen.findByRole("button", { name: "Start new shell" }));

    await waitFor(() => expect(fake.host.open).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("button", { name: "Start new shell" })).toBeNull();
  });

  it("a dead shell host says so and offers Restart", async () => {
    const { fake } = mountDock();
    await waitFor(() => expect(fake.host.open).toHaveBeenCalled());

    act(() => fake.emitHostExited());

    expect(await screen.findByText("[shell host exited — its shells were closed]")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Restart" }));
    await waitFor(() => expect(fake.host.open).toHaveBeenCalledTimes(2));
  });

  it("a shell taken by another window says so and Reattach re-opens it", async () => {
    const { fake } = mountDock();
    await waitFor(() => expect(fake.host.open).toHaveBeenCalled());

    act(() => fake.emitDetached("shell-ses_1"));

    expect(await screen.findByText("[shell opened in another window]")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reattach" }));
    await waitFor(() => expect(fake.host.open).toHaveBeenCalledTimes(2));
    expect(fake.host.end).not.toHaveBeenCalled();
  });
});
