import type { ShellActivity } from "./shellHostCore";

/**
 * D7's quit decision, kept free of Electron so it is unit tested with fakes
 * (quitGuard.test.ts); main.ts supplies the real host, dialog and quit.
 *
 * On `before-quit`, if a shell host is running and the quit isn't already
 * confirmed, the quit is held while the host says what is running. Idle shells
 * end quietly with the host. If anything is busy, ONE confirmation lists it,
 * and only "Quit and end them" goes on. A host that doesn't answer within the
 * timeout counts as idle: quitting is never blocked forever by a wedged helper.
 */

export const INSPECT_TIMEOUT_MS = 1500;

export interface QuitGuardDeps {
  /** Whether a shell host process currently exists. */
  hostRunning: () => boolean;
  /** Asks the host what is running. May reject or never settle. */
  inspect: () => Promise<ShellActivity[]>;
  /** Shows the confirmation for these busy shells; resolves true for "Quit and end them". */
  confirm: (busy: ShellActivity[]) => Promise<boolean>;
  /**
   * Ends every shell (with their descendants) and stops the host. Awaited, so
   * the quit follows once the shells have been hung up.
   */
  killHost: () => void | Promise<void>;
  quit: () => void;
  /** Runs `fn` after `ms` and returns a canceller; defaults to setTimeout. */
  schedule?: (fn: () => void, ms: number) => () => void;
  timeoutMs?: number;
}

export class QuitGuard {
  private confirmed = false;
  private asking = false;

  constructor(private readonly deps: QuitGuardDeps) {}

  /** The `before-quit` handler. Calls preventDefault only when it takes the decision over. */
  handleBeforeQuit(event: { preventDefault(): void }): void {
    if (this.confirmed) return; // our own app.quit() coming back around
    if (!this.deps.hostRunning()) return;
    event.preventDefault();
    // Another quit request (a second Cmd+Q) while the first is still deciding.
    if (this.asking) return;
    this.asking = true;
    void this.decide().finally(() => {
      this.asking = false;
    });
  }

  private async decide(): Promise<void> {
    const shells = await this.inspectWithTimeout();
    const busy = shells.filter((s) => s.busy);
    if (busy.length > 0 && !(await this.deps.confirm(busy))) return;
    this.confirmed = true;
    await this.deps.killHost();
    this.deps.quit();
  }

  private inspectWithTimeout(): Promise<ShellActivity[]> {
    const schedule =
      this.deps.schedule ??
      ((fn, ms) => {
        const timer = setTimeout(fn, ms);
        return () => clearTimeout(timer);
      });
    return new Promise((resolve) => {
      const cancel = schedule(() => resolve([]), this.deps.timeoutMs ?? INSPECT_TIMEOUT_MS);
      this.deps.inspect().then(
        (shells) => {
          cancel();
          resolve(shells);
        },
        () => {
          cancel();
          resolve([]);
        },
      );
    });
  }
}

/** Text for the native dialog: one line per busy shell, by session id (main has no names). */
export function describeBusyShells(busy: ShellActivity[]): string {
  return busy
    .map((s) => {
      const first = s.processes[0]?.command ?? s.foreground ?? "a running process";
      const more = s.processes.length > 1 ? ` (+${s.processes.length - 1} more)` : "";
      return `${s.sessionId}: ${first}${more}`;
    })
    .join("\n");
}
