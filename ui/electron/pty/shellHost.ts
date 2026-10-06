import type { MessagePortMain } from "electron";
import { createProcessSource } from "./processTable";
import { loadNodePty } from "./ptyHost";
import { ShellHostCore, type ShellRequest } from "./shellHostCore";

/**
 * Entry point of the shell host — an Electron utilityProcess forked lazily by
 * main.ts on the first shell request. It (alone, for docked shells) loads
 * node-pty, so a chatty shell or a native crash costs only this process, not
 * the main process's menus, dialogs and IPC. See docs/SHELL-DOCK-DESIGN.md D1.
 *
 * This file is only glue: main sends `{type: "connect", windowId}` with one
 * end of a MessageChannelMain for each window (shell traffic then flows
 * straight between the window's preload and this process), and
 * `{type: "window-gone", windowId}` when that window reloads, crashes or
 * closes. Everything else is ShellHostCore. Requests arrive on a window's
 * port as `{id?, ...ShellRequest}` and are answered with `{id, ok, result}` or
 * `{id, ok: false, error}`; the core's events are posted unsolicited, and an
 * open's held live output follows its reply (see ShellHostCore.handleAndReply).
 *
 * The shells die with this process (node-pty SIGHUPs them), and this process
 * dies with the app.
 */

// Which shells exist goes to main (not to window ports) so windows can learn
// it without connecting to this process — see shellRegistryCache.ts.
const core = new ShellHostCore({
  loadPty: loadNodePty,
  // The real process table and process.kill live only here (and in the
  // realtests): ShellHostCore has no defaults for them on purpose.
  processSource: createProcessSource,
  signal: (pid, sig) => process.kill(pid, sig),
  onShellsChanged: (shells) => process.parentPort?.postMessage({ type: "shells-changed", shells }),
});
const ports = new Map<number, MessagePortMain>();

function dropPort(windowId: number): void {
  core.disconnect(windowId);
  ports.get(windowId)?.close();
  ports.delete(windowId);
}

function connectWindow(windowId: number, port: MessagePortMain): void {
  dropPort(windowId);
  ports.set(windowId, port);
  core.connect(windowId, (event) => port.postMessage(event));
  port.on("message", (message) => {
    const { id, ...request } = message.data as { id?: number } & ShellRequest;
    // No id means fire-and-forget (write/resize): nobody is waiting for a reply.
    // The core posts through `reply` itself so a re-open's held output goes out
    // only after the reply that carries its snapshot and shellId.
    void core.handleAndReply(windowId, request as ShellRequest, (outcome) => {
      if (id !== undefined) port.postMessage({ id, ...outcome });
    });
  });
  // A renderer that goes away closes its end; treat that like window-gone,
  // unless a newer connection for the same window id already replaced us.
  port.on("close", () => {
    if (ports.get(windowId) === port) dropPort(windowId);
  });
  port.start();
}

// process.parentPort exists only inside a utilityProcess; anywhere else
// (vitest importing this by accident) this file stays inert.
if (process.parentPort) {
  process.parentPort.on("message", (event) => {
    const message = event.data as { type: string; windowId: number; requestId?: number };
    if (message.type === "inspect") {
      // Main's quit guard (quitGuard.ts) asks what is running, over the parent
      // port rather than a window port. A failure answers "nothing": it must
      // not block quitting.
      const requestId = message.requestId;
      core.inspect().then(
        (shells) => process.parentPort?.postMessage({ type: "inspect-result", requestId, shells }),
        () => process.parentPort?.postMessage({ type: "inspect-result", requestId, shells: [] }),
      );
    } else if (message.type === "end-all") {
      // Main is shutting the host down: hang up every shell and its jobs first
      // (ShellHostCore.endAll), then say so. Main kills us anyway on timeout.
      const requestId = message.requestId;
      void core.endAll().finally(() => process.parentPort?.postMessage({ type: "end-all-done", requestId }));
    } else if (message.type === "connect" && event.ports[0]) {
      connectWindow(message.windowId, event.ports[0]);
    } else if (message.type === "window-gone") {
      dropPort(message.windowId);
    }
  });
}

// Backstop: if the host is killed without an end-all (Electron's own
// shutdown), still hang up the shells' jobs on the way out.
if (process.parentPort) {
  process.on("SIGTERM", () => {
    void core.endAll().finally(() => process.exit(0));
  });
}
