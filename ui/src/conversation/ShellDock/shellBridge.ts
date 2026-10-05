import type { PtyBridge, PtySpawnOptions, ShellHostBridge } from "../TerminalRenderer/ptyBridge";

/**
 * Scrollback of the dock's xterm. Must equal SHELL_SCROLLBACK in
 * ui/electron/pty/shellMirror.ts (the shell host's mirror): the snapshot a
 * re-open returns is the mirror's whole scrollback, and a smaller terminal
 * would silently drop the oldest lines of it. Duplicated rather than imported
 * because nothing under ui/src/** may import from electron/.
 */
export const SHELL_SCROLLBACK = 1000;

/** What ShellDock needs beyond a plain PtyBridge: ending the shell, and the two ways a shell is lost under the view. */
export interface ShellDockBridge extends PtyBridge {
  /** Kills the session's shell for good (the dock's ×). Hiding never calls this. */
  end(): void;
  /** Another window opened this session's shell and took it over. */
  onDetached(listener: () => void): () => void;
  /** The shell host died; the shell is gone. */
  onHostExited(listener: () => void): () => void;
}

/**
 * PtyBridge over the shell host (window.mads.shell) for ONE session's dock
 * shell (docs/SHELL-DOCK-DESIGN.md D2). It lets TerminalView stay unaware
 * that the process outlives the view: `spawn` is `open` (attach to the
 * session's shell or start one) and `kill` is `detach` — TerminalView calls
 * it on unmount, and unmounting is hiding, never ending. Ending is the
 * separate `end()`.
 */
export class ShellBridge implements ShellDockBridge {
  private shellId: string | undefined;
  // What the open reported for the view's first paint (the live screen of a
  // re-opened shell, or a note that the last one died unseen). TerminalView
  // subscribes only after spawn resolves, so it waits here for that subscription.
  private firstPaint: string[] = [];
  // spawn()s not yet matched by a kill(). A remount can issue its open before
  // the previous mount's kill runs (React StrictMode's mount/unmount/mount,
  // or hiding and re-showing within one open round trip); both resolve to the
  // same shellId, so only the last kill may detach.
  private views = 0;
  // The shell is gone or no longer ours: a detach would be pointless, and
  // after a host exit it would even fork a new host just to say so.
  private lost = false;
  // The newest open still in flight (settled either way, so it is safe to
  // wait on). end() before it resolves has no shell id yet and must wait for
  // it, or the shell would start after the dock was closed.
  private opening: Promise<void> | undefined;
  private readonly detachedListeners = new Set<() => void>();
  private unsubDetached: (() => void) | undefined;

  constructor(
    private readonly host: ShellHostBridge,
    private readonly sessionId: string,
  ) {}

  async spawn(options?: PtySpawnOptions): Promise<string> {
    this.views += 1;
    const request = this.host.open(this.sessionId, {
      cwd: options?.cwd,
      cols: options?.cols,
      rows: options?.rows,
    });
    const settled = request.then(
      () => undefined,
      () => undefined,
    );
    this.opening = settled;
    void settled.then(() => {
      if (this.opening === settled) this.opening = undefined;
    });
    try {
      const result = await request;
      this.shellId = result.shellId;
      this.lost = false;
      this.firstPaint = [];
      if (result.previousExit) {
        // Dim, on its own line, ahead of the fresh shell's prompt.
        this.firstPaint.push(
          `\x1b[2m[previous shell exited with code ${result.previousExit.exitCode}]\x1b[0m\r\n`,
        );
      }
      if (result.snapshot) this.firstPaint.push(result.snapshot);
      this.unsubDetached?.();
      this.unsubDetached = this.host.onDetached(result.shellId, () => {
        this.lost = true;
        this.detachedListeners.forEach((listener) => listener());
      });
      return result.shellId;
    } catch (err) {
      this.views -= 1;
      throw err;
    }
  }

  write(ptyId: string, data: string): void {
    this.host.write(ptyId, data);
  }

  resize(ptyId: string, cols: number, rows: number): void {
    this.host.resize(ptyId, cols, rows);
  }

  kill(ptyId: string): void {
    this.views = Math.max(0, this.views - 1);
    if (this.views > 0 || this.lost) return;
    // The detach is a courtesy to the host (it stops batching output for us);
    // a failure leaves nothing to do.
    this.host.detach(ptyId).catch(() => undefined);
  }

  end(): void {
    if (this.opening) {
      // Not attached yet: end whatever that open produces. If it failed there is no shell to end.
      void this.opening.then(() => this.endNow());
      return;
    }
    this.endNow();
  }

  private endNow(): void {
    const id = this.shellId;
    if (!id || this.lost) return;
    this.lost = true;
    this.host.end(id).catch(() => undefined);
  }

  onData(ptyId: string, listener: (data: string) => void): () => void {
    // Flush synchronously: the first paint must precede any live chunk, and
    // none can arrive in between (port messages are macrotasks).
    const chunks = this.firstPaint;
    this.firstPaint = [];
    for (const chunk of chunks) listener(chunk);
    return this.host.onData(ptyId, listener);
  }

  onExit(ptyId: string, listener: (exitCode: number) => void): () => void {
    return this.host.onExit(ptyId, (code) => {
      this.lost = true; // the shell removed itself; nothing to detach from
      listener(code);
    });
  }

  onDetached(listener: () => void): () => void {
    this.detachedListeners.add(listener);
    return () => this.detachedListeners.delete(listener);
  }

  onHostExited(listener: () => void): () => void {
    return this.host.onHostExited(() => {
      this.lost = true;
      listener();
    });
  }
}
