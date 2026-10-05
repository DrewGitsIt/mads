import { vi } from "vitest";
import type { ShellHostBridge, ShellInfo } from "../TerminalRenderer/ptyBridge";

/**
 * In-memory ShellHostBridge for tests: every call is a vi.fn, and the emit*
 * helpers play the shell host's pushes (output, exit, a window taking the
 * shell, the host dying, the shell set changing).
 */
export function makeFakeShellHost() {
  const data = new Map<string, Set<(d: string) => void>>();
  const exit = new Map<string, Set<(c: number) => void>>();
  const detached = new Map<string, Set<() => void>>();
  const hostExited = new Set<() => void>();
  const shellsChanged = new Set<(shells: ShellInfo[]) => void>();
  let shells: ShellInfo[] = [];

  const add = <T>(registry: Map<string, Set<T>>, id: string, listener: T) => {
    const set = registry.get(id) ?? new Set<T>();
    set.add(listener);
    registry.set(id, set);
    return () => set.delete(listener);
  };

  const host = {
    open: vi.fn<ShellHostBridge["open"]>(async (sessionId) => ({
      shellId: `shell-${sessionId}`,
      created: true,
    })),
    detach: vi.fn(async (_shellId: string) => {}),
    end: vi.fn(async (_shellId: string) => {}),
    write: vi.fn(),
    resize: vi.fn(),
    list: vi.fn(async () => shells),
    onData: vi.fn((id: string, l: (d: string) => void) => add(data, id, l)),
    onExit: vi.fn((id: string, l: (c: number) => void) => add(exit, id, l)),
    onDetached: vi.fn((id: string, l: () => void) => add(detached, id, l)),
    onShellsChanged: vi.fn((l: (shells: ShellInfo[]) => void) => {
      shellsChanged.add(l);
      return () => shellsChanged.delete(l);
    }),
    onHostExited: vi.fn((l: () => void) => {
      hostExited.add(l);
      return () => hostExited.delete(l);
    }),
  } satisfies ShellHostBridge;

  return {
    host: host as ShellHostBridge & typeof host,
    emitData: (id: string, d: string) => data.get(id)?.forEach((l) => l(d)),
    emitExit: (id: string, code: number) => exit.get(id)?.forEach((l) => l(code)),
    emitDetached: (id: string) => detached.get(id)?.forEach((l) => l()),
    emitHostExited: () => hostExited.forEach((l) => l()),
    /** Replaces what list() returns and pushes it to every shells-changed listener, as main does. */
    setShells: (next: ShellInfo[]) => {
      shells = next;
      shellsChanged.forEach((l) => l(next));
    },
  };
}
