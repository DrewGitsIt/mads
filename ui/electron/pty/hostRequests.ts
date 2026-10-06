/**
 * Main's request/response over the shell host's parent port: each request
 * carries an id, and the host answers with a message naming it. A request
 * settles once, with the answer, or with undefined if the timeout fires or
 * the host dies first — callers (quit) must never wait on a wedged helper.
 * Free of Electron so the correlation is unit tested (hostRequests.test.ts).
 */
export class HostRequests {
  private nextId = 0;
  private readonly waiters = new Map<number, (result: unknown) => void>();

  constructor(
    private readonly post: (message: { type: string; requestId: number }) => void,
    /** Runs `fn` after `ms` and returns a canceller; defaults to setTimeout. */
    private readonly schedule: (fn: () => void, ms: number) => () => void = (fn, ms) => {
      const timer = setTimeout(fn, ms);
      return () => clearTimeout(timer);
    },
  ) {}

  request<T>(type: string, timeoutMs: number): Promise<T | undefined> {
    const requestId = ++this.nextId;
    return new Promise((resolve) => {
      const settle = (result: unknown) => {
        if (!this.waiters.delete(requestId)) return;
        cancel();
        resolve(result as T | undefined);
      };
      this.waiters.set(requestId, settle);
      const cancel = this.schedule(() => settle(undefined), timeoutMs);
      this.post({ type, requestId });
    });
  }

  /** The host answered request `requestId`. Late or unknown ids are ignored. */
  reply(requestId: number, result: unknown): void {
    this.waiters.get(requestId)?.(result);
  }

  /** The host is gone: nothing more will be answered. */
  failAll(): void {
    for (const settle of [...this.waiters.values()]) settle(undefined);
  }
}
