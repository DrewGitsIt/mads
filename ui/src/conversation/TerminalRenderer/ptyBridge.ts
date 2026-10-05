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

/** The action the user picked out of TerminalContextMenu.showTerminal's native menu. */
export type TerminalMenuAction = "copy" | "paste" | "selectAll" | "clear";

export interface TerminalContextMenu {
  /**
   * Pops a native Electron context menu at the current cursor position and
   * resolves once it closes: the chosen action, or null if dismissed without
   * picking one. The renderer (TerminalView), not main, performs the action —
   * only it holds the Terminal instance and the text under selection.
   */
  showTerminal(state: { hasSelection: boolean }): Promise<TerminalMenuAction | null>;
}

export interface ClipboardBridge {
  readText(): Promise<string>;
  writeText(text: string): Promise<void>;
}

declare global {
  interface Window {
    mads?: {
      pty?: PtyBridge;
      badge?: { setCount(count: number): void };
      dialog?: {
        pickDirectory(options?: { title?: string; defaultPath?: string }): Promise<string | null>;
      };
      contextMenu?: TerminalContextMenu;
      clipboard?: ClipboardBridge;
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

/**
 * Reads the preload-exposed native context menu, undefined wherever `mads`
 * isn't (vitest, a plain browser preview). Callers must fall back to
 * whatever that environment offers on its own (here: no custom menu, letting
 * the browser's default one — if any — show instead).
 */
export function useElectronContextMenu(): TerminalContextMenu | undefined {
  return typeof window !== "undefined" ? window.mads?.contextMenu : undefined;
}

/**
 * Reads the preload-exposed clipboard bridge. Electron's `navigator.clipboard`
 * is unreliable from a BrowserWindow (permission prompts that have nowhere to
 * go), so in Electron this routes through main's `clipboard` module via IPC
 * instead; outside Electron this is undefined and callers fall back to
 * `navigator.clipboard` directly.
 */
export function useElectronClipboardBridge(): ClipboardBridge | undefined {
  return typeof window !== "undefined" ? window.mads?.clipboard : undefined;
}
