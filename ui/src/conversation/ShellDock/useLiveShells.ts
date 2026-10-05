import { useEffect, useState } from "react";
import type { ShellInfo } from "../TerminalRenderer/ptyBridge";

/**
 * Every live dock shell, kept current: lists on mount, then follows each
 * `shells-changed` push (a shell started, ended or exited, or the host died —
 * main pushes the new list, so a dead host arrives as an empty one). Shared by
 * everything that needs to know a shell exists even while its dock is hidden —
 * the session menu, the rail's shell cards, the Settings inventory. Empty
 * outside Electron, where there is no shell host.
 *
 * It never forks the shell host: list and the change push are answered by
 * main's cache of what the host last reported (no host means no shells), not
 * by a port to the host. So mounting this in every session view costs nothing
 * for a user who never opens a shell.
 */
export function useLiveShells(): ShellInfo[] {
  const [shells, setShells] = useState<ShellInfo[]>([]);

  useEffect(() => {
    const host = typeof window !== "undefined" ? window.mads?.shell : undefined;
    if (!host) return;
    let cancelled = false;
    // A push is newer than any list() still in flight; only the newest answer may land.
    let latest = 0;
    const apply = (next: ShellInfo[]) => {
      if (!cancelled) setShells(next);
    };
    const request = ++latest;
    host
      .list()
      .then((next) => {
        if (request === latest) apply(next);
      })
      .catch(() => undefined);
    const unsubscribe = host.onShellsChanged((next) => {
      latest += 1;
      apply(next);
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  return shells;
}
