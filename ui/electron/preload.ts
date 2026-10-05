import { contextBridge, ipcRenderer } from "electron";

interface PtySpawnOptions {
  cwd?: string;
  cols?: number;
  rows?: number;
  shell?: string;
}

/**
 * Renderer-facing pty namespace — mirrors the PtyBridge interface in
 * ui/src/conversation/TerminalRenderer/ptyBridge.ts. Wraps ipcRenderer so no
 * raw ipcRenderer/node-pty ever crosses into the renderer (contextIsolation
 * stays on in main.ts).
 */
const pty = {
  spawn: (options?: PtySpawnOptions): Promise<string> =>
    ipcRenderer.invoke("pty:spawn", options),
  write: (id: string, data: string): void => {
    void ipcRenderer.invoke("pty:write", { id, data });
  },
  resize: (id: string, cols: number, rows: number): void => {
    void ipcRenderer.invoke("pty:resize", { id, cols, rows });
  },
  kill: (id: string): void => {
    void ipcRenderer.invoke("pty:kill", { id });
  },
  onData: (id: string, listener: (data: string) => void): (() => void) => {
    const handler = (_event: unknown, payload: { id: string; data: string }) => {
      if (payload.id === id) listener(payload.data);
    };
    ipcRenderer.on("pty:data", handler);
    return () => ipcRenderer.removeListener("pty:data", handler);
  },
  onExit: (id: string, listener: (exitCode: number) => void): (() => void) => {
    const handler = (_event: unknown, payload: { id: string; exitCode: number }) => {
      if (payload.id === id) listener(payload.exitCode);
    };
    ipcRenderer.on("pty:exit", handler);
    return () => ipcRenderer.removeListener("pty:exit", handler);
  },
};

/**
 * Renderer-facing badge namespace — mirrors the BadgeBridge interface in
 * ui/src/features/command-palette/badgeBridge.ts.
 */
const badge = {
  setCount: (count: number): void => {
    void ipcRenderer.invoke("badge:set-count", count);
  },
};

/**
 * Renderer-facing dialog namespace — mirrors the DirectoryPicker interface in
 * ui/src/platform/directoryPicker.ts. Resolves null when the user cancels.
 */
const dialog = {
  pickDirectory: (options?: { title?: string; defaultPath?: string }): Promise<string | null> =>
    ipcRenderer.invoke("dialog:pick-directory", options),
};

contextBridge.exposeInMainWorld("mads", { pty, badge, dialog });
