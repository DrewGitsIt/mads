import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import type { PtyBridge, PtySpawnOptions } from "./ptyBridge";
import "@xterm/xterm/css/xterm.css";
import styles from "./TerminalView.module.css";

export interface TerminalViewProps {
  bridge: PtyBridge;
  spawnOptions?: PtySpawnOptions;
  className?: string;
}

/**
 * xterm.js mounted over a PtyBridge. Used both for the Conversation tab's
 * terminal display mode (bound to the agent's own PTY session) and
 * ShellDock's docked general-purpose shell (bound to a separate pty) — each
 * caller supplies its own bridge/pty id (HANDOFF.md §4).
 */
export function TerminalView({ bridge, spawnOptions, className }: TerminalViewProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const term = new Terminal({ convertEol: true });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(container);
    fitAddon.fit();

    let ptyId: string | undefined;
    let disposed = false;
    let unsubData: (() => void) | undefined;
    let unsubExit: (() => void) | undefined;

    const dataDisposable = term.onData((data) => {
      if (ptyId) bridge.write(ptyId, data);
    });

    const resizeObserver = new ResizeObserver(() => {
      fitAddon.fit();
      if (ptyId) bridge.resize(ptyId, term.cols, term.rows);
    });
    resizeObserver.observe(container);

    bridge
      .spawn({ ...spawnOptions, cols: term.cols, rows: term.rows })
      .then((id) => {
        if (disposed) {
          bridge.kill(id);
          return;
        }
        ptyId = id;
        unsubData = bridge.onData(id, (data) => term.write(data));
        unsubExit = bridge.onExit(id, () => {
          term.write("\r\n[process exited]\r\n");
        });
      })
      .catch((err: unknown) => {
        if (!disposed) {
          term.write(`\r\n[pty unavailable: ${String(err)}]\r\n`);
        }
      });

    return () => {
      disposed = true;
      resizeObserver.disconnect();
      dataDisposable.dispose();
      unsubData?.();
      unsubExit?.();
      if (ptyId) bridge.kill(ptyId);
      term.dispose();
    };
    // A bridge identity change (e.g. ConversationTab reconnecting its
    // SessionEventSource) must re-run this effect so onData subscribes to
    // the *current* bridge — otherwise the terminal silently listens on a
    // stale, disconnected bridge forever (only a full remount used to fix
    // this). spawnOptions is still deliberately excluded: callers like
    // ShellDock pass a fresh object literal every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- spawnOptions identity is intentionally excluded
  }, [bridge]);

  return (
    <div
      ref={containerRef}
      data-testid="terminal-view"
      className={[styles.terminal, className].filter(Boolean).join(" ")}
    />
  );
}
