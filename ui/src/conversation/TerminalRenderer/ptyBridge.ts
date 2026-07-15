/**
 * Renderer-side contract for a PTY transport. `TerminalView` and everything
 * under ui/src/conversation/ talk only to this interface — never to
 * node-pty or Electron IPC directly (see ui/src/conversation/HANDOFF.md
 * §5). Two call sites (the Conversation tab's terminal display mode and
 * ShellDock's general shell) each get their own PtyBridge instance / pty
 * id; never share one pty between them.
 */
export interface PtySpawnOptions {
  cwd?: string;
  cols?: number;
  rows?: number;
  shell?: string;
}

export interface PtyBridge {
  spawn(options?: PtySpawnOptions): Promise<string>;
  write(ptyId: string, data: string): void;
  resize(ptyId: string, cols: number, rows: number): void;
  kill(ptyId: string): void;
  /** Returns an unsubscribe function. */
  onData(ptyId: string, listener: (data: string) => void): () => void;
  /** Returns an unsubscribe function. */
  onExit(ptyId: string, listener: (exitCode: number) => void): () => void;
}

declare global {
  interface Window {
    mads?: {
      pty?: PtyBridge;
      badge?: { setCount(count: number): void };
    };
  }
}

/**
 * Reads the PtyBridge the preload script exposed on `window.mads.pty`.
 * Returns undefined outside Electron (e.g. the `npm run dev` renderer-only
 * server, or any vitest environment) — callers must handle that case, they
 * never get a node-pty import transitively through this module.
 */
export function useElectronPtyBridge(): PtyBridge | undefined {
  return typeof window !== "undefined" ? window.mads?.pty : undefined;
}
