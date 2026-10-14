import { useEffect, useRef, useState } from "react";
import { ResizablePanel } from "../../components/ResizablePanel/ResizablePanel";
import { TerminalView, type TerminalViewHandle } from "../TerminalRenderer/TerminalView";
import type { ShellActivity } from "../TerminalRenderer/ptyBridge";
import { requestOpenSettings } from "../../appNavigation";
import { EndShellDialog } from "./EndShellDialog";
import { useLiveShells } from "./useLiveShells";
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
  /** ×, once confirmed (or at once when the shell is idle): end the shell and close the dock. */
  onEnd?: () => void;
}

/**
 * How many live shells make the dock say so (D8). Only a notice: nothing is
 * capped and nothing is ever ended on the user's behalf.
 */
export const SHELL_SOFT_LIMIT = 9;

/** Why the terminal under the dock no longer has a live shell attached. */
type Lost = "exited" | "host-exited" | "detached";

/**
 * Docked, resizable general-purpose shell (Ctrl+\`) — a separate process from
 * the agent's own PTY session. Callers supply a ShellBridge for this session;
 * the shell it attaches to outlives this component (hiding the dock unmounts
 * it, which detaches), so only `onEnd` — the × — kills it
 * (docs/SHELL-DOCK-DESIGN.md D2/D4). The × first asks the bridge what is
 * running: an idle shell ends at once, a busy one asks first (D4).
 *
 * Opening the dock puts focus in the terminal — a new shell or a hidden one
 * coming back — so typing goes straight to it; so does each remount below.
 *
 * When the shell goes away under a mounted dock, a strip under the terminal
 * says why and offers the way back: each button remounts TerminalView, whose
 * mount is an `open` that starts or re-takes the session's shell.
 */
export function ShellDock({ bridge, cwd, height, onHeightChange, onHide, onEnd }: ShellDockProps) {
  const [lost, setLost] = useState<Lost>();
  // Bumped to remount TerminalView for a fresh open.
  const [generation, setGeneration] = useState(0);
  // The shell's activity while the End dialog is up; undefined means no dialog.
  const [confirming, setConfirming] = useState<ShellActivity>();
  // The × is in flight (asking the host); a second click mustn't stack another ask.
  const asking = useRef(false);
  // The soft-limit notice (D8), dismissed for this dock's lifetime only.
  const liveCount = useLiveShells().length;
  const [limitDismissed, setLimitDismissed] = useState(false);
  const terminalRef = useRef<TerminalViewHandle>(null);

  // TerminalView's own mount effect has created the xterm by the time this
  // (parent) effect runs, so focus lands on a live terminal.
  useEffect(() => {
    terminalRef.current?.focus();
  }, [generation]);

  useEffect(() => {
    const unsubDetached = bridge.onDetached(() => setLost("detached"));
    const unsubHostExited = bridge.onHostExited(() => setLost("host-exited"));
    return () => {
      unsubDetached();
      unsubHostExited();
    };
  }, [bridge]);

  const requestEnd = async () => {
    if (asking.current) return;
    asking.current = true;
    try {
      const activity = await bridge.inspect().catch(() => undefined);
      // Idle, or the host can't say (gone): ending is what × always did.
      if (activity?.busy) setConfirming(activity);
      else onEnd?.();
    } finally {
      asking.current = false;
    }
  };

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
          {liveCount >= SHELL_SOFT_LIMIT && !limitDismissed ? (
            <span className={styles.limit} role="status">
              {liveCount} shells are running ·{" "}
              <button
                type="button"
                className={styles.limitLink}
                onClick={() => requestOpenSettings("shells")}
              >
                Manage
              </button>
              <button
                type="button"
                className={styles.limitLink}
                aria-label="Dismiss shell count notice"
                onClick={() => setLimitDismissed(true)}
              >
                ×
              </button>
            </span>
          ) : (
            <span className={styles.spacer} />
          )}
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
            onClick={() => void requestEnd()}
          >
            ×
          </button>
        </div>
        <TerminalView
          key={generation}
          ref={terminalRef}
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
      <EndShellDialog
        activity={confirming}
        cancelLabel="Keep running, hide"
        onConfirm={() => {
          setConfirming(undefined);
          onEnd?.();
        }}
        onCancel={() => {
          setConfirming(undefined);
          onHide?.();
        }}
        onDismiss={() => setConfirming(undefined)}
      />
    </ResizablePanel>
  );
}
