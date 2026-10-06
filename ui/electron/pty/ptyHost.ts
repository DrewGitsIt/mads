import { isAbsolute } from "node:path";
import os from "node:os";

/**
 * node-pty loading and spawn helpers for the shell host (shellHostCore.ts).
 * node-pty is a native addon compiled against a specific Node ABI —
 * Electron's main process runs its own bundled Node, distinct from whatever
 * Node runs vitest. This file is the ONLY place that
 * imports node-pty, and only via a dynamic import wrapped in try/catch, so
 * a missing/mismatched native binary never breaks `npm test`/`make check`
 * (see ui/src/conversation/HANDOFF.md §5). Nothing under ui/src/** may
 * import this module or node-pty, directly or transitively.
 */

export interface PtyProcess {
  pid?: number;
  /**
   * Name of the pty's foreground process (node-pty reads it on Linux and
   * macOS). It equals the shell's own name while the shell sits at its prompt.
   */
  readonly process?: string;
  write(data: string): void;
  resize(cols: number, rows: number): void;
  kill(): void;
  onData(listener: (data: string) => void): { dispose(): void };
  onExit(listener: (e: { exitCode: number }) => void): { dispose(): void };
}

export interface NodePtyModule {
  spawn(
    file: string,
    args: string[] | string,
    options: {
      name?: string;
      cols?: number;
      rows?: number;
      cwd?: string;
      env?: Record<string, string | undefined>;
    },
  ): PtyProcess;
}

let nodePtyPromise: Promise<NodePtyModule | undefined> | undefined;

/** Loads node-pty once, memoized; resolves to undefined if unavailable. */
export function loadNodePty(): Promise<NodePtyModule | undefined> {
  if (!nodePtyPromise) {
    nodePtyPromise = import("node-pty")
      .then((mod) => mod as unknown as NodePtyModule)
      .catch(() => undefined);
  }
  return nodePtyPromise;
}

export function defaultShell(): string {
  if (process.platform === "win32") return "powershell.exe";
  // Prefer $SHELL (set when the app is launched from a terminal), but fall back
  // to the user's login shell from the passwd db — a GUI launch leaves $SHELL
  // unset, and hardcoding bash there ignores the user's real default (zsh, fish…).
  return process.env.SHELL || os.userInfo().shell || "/bin/bash";
}

/**
 * Resolves the cwd a spawn request asked for. Only an absolute path is
 * honored: a relative one (notably the cwd "." that sessions started before
 * recordedCwd in services/session/internal/runner still carry) would resolve against
 * Electron main's own process.cwd(), which is wherever `npm --prefix ui
 * start` launched from (the checkout's ui/ dir), not anywhere the user would
 * recognize. Falling back to HOME matches a plain terminal's own default.
 */
export function resolveCwd(cwd: string | undefined): string | undefined {
  if (cwd && isAbsolute(cwd)) return cwd;
  return process.env.HOME;
}
