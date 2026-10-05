import { useEffect, useState } from "react";
import { ResizablePanel } from "../../components/ResizablePanel/ResizablePanel";
import { TerminalView } from "../TerminalRenderer/TerminalView";
import { SHELL_SCROLLBACK, type ShellDockBridge } from "./shellBridge";
import styles from "./ShellDock.module.css";

export interface ShellDockProps {
  bridge: ShellDockBridge;
  cwd?: string;
  /** Stored height (the dock's per-session state); the panel starts at it. */
  height?: number;
  onHeightChange?: (height: number) => void;
  /** Chevron: hide the dock. The shell keeps running. */
  onHide?: () => void;
  /** ×: end the shell and close the dock. */
  onEnd?: () => void;
}

/** Why the terminal under the dock no longer has a live shell attached. */
type Lost = "exited" | "host-exited" | "detached";

/**
 * Docked, resizable general-purpose shell (Ctrl+\`) — a separate process from
 * the agent's own PTY session. Callers supply a ShellBridge for this session;
 * the shell it attaches to outlives this component (hiding the dock unmounts
 * it, which detaches), so only `onEnd` — the × — kills it
 * (docs/SHELL-DOCK-DESIGN.md D2/D4).
 *
 * When the shell goes away under a mounted dock, a strip under the terminal
 * says why and offers the way back: each button remounts TerminalView, whose
 * mount is an `open` that starts or re-takes the session's shell.
 */
export function ShellDock({ bridge, cwd, height, onHeightChange, onHide, onEnd }: ShellDockProps) {
  const [lost, setLost] = useState<Lost>();
  // Bumped to remount TerminalView for a fresh open.
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const unsubDetached = bridge.onDetached(() => setLost("detached"));
    const unsubHostExited = bridge.onHostExited(() => setLost("host-exited"));
    return () => {
      unsubDetached();
      unsubHostExited();
    };
  }, [bridge]);

  const reopen = () => {
    setLost(undefined);
    setGeneration((g) => g + 1);
  };

  return (
    <ResizablePanel
      direction="vertical"
      defaultSize={height}
      onResizeEnd={onHeightChange}
      className={styles.dock}
    >
      <div className={styles.body}>
        <div className={styles.header}>
          <span className={styles.title}>Shell</span>
          <button
            type="button"
            className={styles.headerButton}
            aria-label="Hide shell"
            title="Hide shell (Ctrl+`) — it keeps running"
            onClick={onHide}
          >
            ⌄
          </button>
          <button
            type="button"
            className={styles.headerButton}
            aria-label="End shell"
            title="End shell"
            onClick={onEnd}
          >
            ×
          </button>
        </div>
        <TerminalView
          key={generation}
          bridge={bridge}
          className={styles.terminal}
          spawnOptions={cwd ? { cwd } : undefined}
          scrollback={SHELL_SCROLLBACK}
          onExit={() => setLost("exited")}
        />
        {lost && (
          <div className={styles.notice} role="status">
            {lost === "host-exited" && (
              <span>[shell host exited — its shells were closed]</span>
            )}
            {lost === "detached" && <span>[shell opened in another window]</span>}
            {lost === "exited" && <span>The shell has exited.</span>}
            <button type="button" className={styles.noticeButton} onClick={reopen}>
              {lost === "exited"
                ? "Start new shell"
                : lost === "host-exited"
                  ? "Restart"
                  : "Reattach"}
            </button>
          </div>
        )}
      </div>
    </ResizablePanel>
  );
}
