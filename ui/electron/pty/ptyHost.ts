import { randomUUID } from "node:crypto";
import { isAbsolute } from "node:path";
import os from "node:os";
import type { IpcMain, WebContents } from "electron";
import { shellEnv } from "./shellEnv";

/**
 * PTY lifecycle, shared by the in-process pty:* handlers and the shell host.
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

export interface PtySpawnOptions {
  cwd?: string;
  cols?: number;
  rows?: number;
  shell?: string;
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

/**
 * Registers the pty:* IPC handlers the preload/renderer talk to. Call it
 * ONCE at app startup (ipcMain.handle throws on a duplicate channel, and
 * macOS re-runs createWindow from "activate"), so nothing here may capture a
 * window: each pty remembers the webContents that spawned it (event.sender)
 * and streams data back to that one. The host tracks its own set of live
 * ptys, keyed by a generated id — the renderer never sees anything node-pty
 * specific.
 *
 * This in-process path is what ShellDock uses until it moves to the shell
 * host (see shellHost.ts and docs/SHELL-DOCK-DESIGN.md).
 */
export function registerPtyHandlers(ipcMain: IpcMain): void {
  const ptys = new Map<string, PtyProcess>();
  // One "destroyed" hook per webContents, however many ptys it spawns.
  const hooked = new WeakSet<WebContents>();
  const ownedBy = new Map<WebContents, Set<string>>();

  const send = (target: WebContents, channel: string, payload: unknown) => {
    if (!target.isDestroyed()) target.send(channel, payload);
  };

  ipcMain.handle("pty:spawn", async (event, options?: PtySpawnOptions) => {
    const nodePty = await loadNodePty();
    if (!nodePty) {
      throw new Error("node-pty is unavailable on this machine");
    }
    const sender = event.sender;
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
    let owned = ownedBy.get(sender);
    if (!owned) ownedBy.set(sender, (owned = new Set()));
    owned.add(id);
    if (!hooked.has(sender)) {
      hooked.add(sender);
      // A closed window must not leave its shells running.
      sender.once("destroyed", () => {
        for (const ownedId of ownedBy.get(sender) ?? []) {
          ptys.get(ownedId)?.kill();
          ptys.delete(ownedId);
        }
        ownedBy.delete(sender);
      });
    }
    proc.onData((data) => {
      send(sender, "pty:data", { id, data });
    });
    proc.onExit(({ exitCode }) => {
      send(sender, "pty:exit", { id, exitCode });
      ptys.delete(id);
      ownedBy.get(sender)?.delete(id);
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
    for (const owned of ownedBy.values()) owned.delete(id);
  });
}
