import { ResizablePanel } from "../../components/ResizablePanel/ResizablePanel";
import { TerminalView } from "../TerminalRenderer/TerminalView";
import type { PtyBridge } from "../TerminalRenderer/ptyBridge";
import styles from "./ShellDock.module.css";

export interface ShellDockProps {
  bridge: PtyBridge;
  cwd?: string;
}

/**
 * Docked, resizable general-purpose shell (Ctrl+\`) — a separate worktree
 * process, nothing to do with the agent's own PTY session. Callers must
 * supply a `bridge` distinct from the one bound to the Conversation tab's
 * terminal display mode; TerminalView spawns its own pty id per mount, so
 * never share a mounted TerminalView between the two (HANDOFF.md §5).
 */
export function ShellDock({ bridge, cwd }: ShellDockProps) {
  return (
    <ResizablePanel direction="vertical" defaultSize={280} className={styles.dock}>
      <TerminalView bridge={bridge} spawnOptions={cwd ? { cwd } : undefined} />
    </ResizablePanel>
  );
}
