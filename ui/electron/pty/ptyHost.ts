import { randomUUID } from "node:crypto";
import { isAbsolute } from "node:path";
import os from "node:os";
import type { IpcMain, WebContents } from "electron";
import { shellEnv } from "./shellEnv";

/**
 * Main-process PTY lifecycle. node-pty is a native addon compiled against a
 * specific Node ABI — Electron's main process runs its own bundled Node,
 * distinct from whatever Node runs vitest. This file is the ONLY place that
 * imports node-pty, and only via a dynamic import wrapped in try/catch, so
 * a missing/mismatched native binary never breaks `npm test`/`make check`
 * (see ui/src/conversation/HANDOFF.md §5). Nothing under ui/src/** may
 * import this module or node-pty, directly or transitively.
 */

interface PtyProcess {
  write(data: string): void;
  resize(cols: number, rows: number): void;
  kill(): void;
  onData(listener: (data: string) => void): { dispose(): void };
  onExit(listener: (e: { exitCode: number }) => void): { dispose(): void };
}

interface NodePtyModule {
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
function loadNodePty(): Promise<NodePtyModule | undefined> {
  if (!nodePtyPromise) {
    nodePtyPromise = import("node-pty")
      .then((mod) => mod as unknown as NodePtyModule)
      .catch(() => undefined);
  }
  return nodePtyPromise;
}

export interface PtySpawnOptions {
  cwd?: string;
  cols?: number;
  rows?: number;
  shell?: string;
}

function defaultShell(): string {
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

/**
 * Registers the pty:* IPC handlers this window's preload/renderer talk to.
 * One instance of this host tracks its own set of live ptys, keyed by a
 * generated id — the renderer never sees anything node-pty specific.
 */
export function registerPtyHandlers(ipcMain: IpcMain, webContents: WebContents): void {
  const ptys = new Map<string, PtyProcess>();

  ipcMain.handle("pty:spawn", async (_event, options?: PtySpawnOptions) => {
    const nodePty = await loadNodePty();
    if (!nodePty) {
      throw new Error("node-pty is unavailable on this machine");
    }
    const shell = options?.shell ?? defaultShell();
    const proc = nodePty.spawn(shell, [], {
      name: "xterm-256color",
      cols: options?.cols ?? 80,
      rows: options?.rows ?? 24,
      cwd: resolveCwd(options?.cwd),
      env: shellEnv(process.env),
    });
    const id = randomUUID();
    ptys.set(id, proc);
    proc.onData((data) => {
      webContents.send("pty:data", { id, data });
    });
    proc.onExit(({ exitCode }) => {
      webContents.send("pty:exit", { id, exitCode });
      ptys.delete(id);
    });
    return id;
  });

  ipcMain.handle("pty:write", (_event, { id, data }: { id: string; data: string }) => {
    ptys.get(id)?.write(data);
  });

  ipcMain.handle(
    "pty:resize",
    (_event, { id, cols, rows }: { id: string; cols: number; rows: number }) => {
      ptys.get(id)?.resize(cols, rows);
    },
  );

  ipcMain.handle("pty:kill", (_event, { id }: { id: string }) => {
    ptys.get(id)?.kill();
    ptys.delete(id);
  });
}
