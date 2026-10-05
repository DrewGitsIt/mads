import type { ShellInfo } from "./shellHostCore";

/**
 * Main's copy of "which shells exist", fed by the shell host's
 * `shells-changed` posts. It exists so windows can ask the question (and hear
 * about changes) through main without connecting to the host: asking the host
 * would fork it, and most sessions never open a shell. With no host the cache
 * is simply empty, which is the truth. `onChange` is how main fans a change
 * out to every window.
 */
export class ShellRegistryCache {
  private shells: ShellInfo[] = [];

  constructor(private readonly onChange: (shells: ShellInfo[]) => void) {}

  get(): ShellInfo[] {
    return this.shells;
  }

  /** The host reported its current list. */
  set(shells: ShellInfo[]): void {
    this.shells = shells;
    this.onChange(shells);
  }

  /** The host is gone, and its shells with it. A no-op (no broadcast) if nothing was cached. */
  clear(): void {
    if (this.shells.length === 0) return;
    this.shells = [];
    this.onChange(this.shells);
  }
}
