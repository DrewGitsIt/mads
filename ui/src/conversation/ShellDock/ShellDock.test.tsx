import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { ShellDock } from "./ShellDock";
import type { PtyBridge } from "../TerminalRenderer/ptyBridge";

/** In-memory PtyBridge stub — no Electron, no IPC, no node-pty. */
function makeStubBridge(): PtyBridge {
  return {
    spawn: vi.fn(async () => "shell-pty-1"),
    write: vi.fn(),
    resize: vi.fn(),
    kill: vi.fn(),
    onData: vi.fn(() => () => {}),
    onExit: vi.fn(() => () => {}),
  };
}

describe("ShellDock", () => {
  it("mounts a resizable panel and spawns its own pty via the bridge", async () => {
    const bridge = makeStubBridge();
    const { container } = render(<ShellDock bridge={bridge} />);

    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));
    expect(container.querySelector('[data-testid="resize-handle"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy();
  });

  it("passes cwd through to the bridge spawn options", async () => {
    const bridge = makeStubBridge();
    render(<ShellDock bridge={bridge} cwd="/repo/worktree" />);

    await waitFor(() =>
      expect(bridge.spawn).toHaveBeenCalledWith(
        expect.objectContaining({ cwd: "/repo/worktree" }),
      ),
    );
  });
});
