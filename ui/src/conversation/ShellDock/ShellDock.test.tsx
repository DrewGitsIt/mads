import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ShellDock } from "./ShellDock";
import { ShellBridge } from "./shellBridge";
import type { ShellActivity } from "../TerminalRenderer/ptyBridge";
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
    await waitFor(() => expect(onEnd).toHaveBeenCalledTimes(1));
  });

  describe("× with something running (D4)", () => {
    const activity = (commands: string[]): ShellActivity => ({
      sessionId: "ses_1",
      shellId: "shell-ses_1",
      busy: commands.length > 0,
      foreground: undefined,
      processes: commands.map((command, i) => ({ pid: 10 + i, command })),
      count: commands.length,
      rssKb: 0,
    });
    const clickEnd = () => fireEvent.click(screen.getByRole("button", { name: "End shell" }));

    it("an idle shell ends at once, with no dialog", async () => {
      const onEnd = vi.fn();
      const { fake } = mountDock({ onEnd });
      await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
      fake.host.inspect.mockResolvedValue([activity([])]);

      clickEnd();

      await waitFor(() => expect(onEnd).toHaveBeenCalledTimes(1));
      expect(fake.host.inspect).toHaveBeenCalledWith("shell-ses_1");
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });

    it("a busy shell asks first, listing what runs and the nohup caveat", async () => {
      const onEnd = vi.fn();
      const { fake } = mountDock({ onEnd });
      await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
      fake.host.inspect.mockResolvedValue([
        activity(["sudo python -m http.server 80", "tail -f /var/log/syslog"]),
      ]);

      clickEnd();

      const dialog = await screen.findByRole("alertdialog");
      expect(within(dialog).getByText("2 processes are still running in this shell")).toBeTruthy();
      expect(within(dialog).getByText("sudo python -m http.server 80")).toBeTruthy();
      expect(within(dialog).getByText("tail -f /var/log/syslog")).toBeTruthy();
      expect(dialog.textContent).toContain("Ending the shell stops them.");
      expect(dialog.textContent).toContain("nohup");
      expect(onEnd).not.toHaveBeenCalled();
    });

    it("Keep running, hide hides without ending", async () => {
      const onHide = vi.fn();
      const onEnd = vi.fn();
      const { fake } = mountDock({ onHide, onEnd });
      await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
      fake.host.inspect.mockResolvedValue([activity(["sleep 30"])]);

      clickEnd();
      fireEvent.click(await screen.findByRole("button", { name: "Keep running, hide" }));

      expect(onHide).toHaveBeenCalledTimes(1);
      expect(onEnd).not.toHaveBeenCalled();
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });

    it("End shell in the dialog ends", async () => {
      const onEnd = vi.fn();
      const { fake } = mountDock({ onEnd });
      await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
      fake.host.inspect.mockResolvedValue([activity(["sleep 30"])]);

      clickEnd();
      const dialog = await screen.findByRole("alertdialog");
      expect(within(dialog).getByText("A process is still running in this shell")).toBeTruthy();
      fireEvent.click(within(dialog).getByRole("button", { name: "End shell" }));

      expect(onEnd).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });

    it("Escape closes the question without hiding or ending", async () => {
      const onHide = vi.fn();
      const onEnd = vi.fn();
      const { fake } = mountDock({ onHide, onEnd });
      await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
      fake.host.inspect.mockResolvedValue([activity(["sleep 30"])]);

      clickEnd();
      await screen.findByRole("alertdialog");
      fireEvent.keyDown(document, { key: "Escape" });

      expect(screen.queryByRole("alertdialog")).toBeNull();
      expect(onHide).not.toHaveBeenCalled();
      expect(onEnd).not.toHaveBeenCalled();
    });

    it("falls back to ending when the host can't be asked", async () => {
      const onEnd = vi.fn();
      const { fake } = mountDock({ onEnd });
      await waitFor(() => expect(fake.host.open).toHaveBeenCalled());
      fake.host.inspect.mockRejectedValue(new Error("shell host exited"));

      clickEnd();

      await waitFor(() => expect(onEnd).toHaveBeenCalledTimes(1));
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });
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
