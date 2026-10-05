import { SerializeAddon } from "@xterm/addon-serialize";
import { Terminal } from "@xterm/headless";

/**
 * A shell's screen mirror (docs/SHELL-DOCK-DESIGN.md D3): a headless xterm fed
 * every pty byte so a view that comes back can be handed the current screen.
 * ShellHostCore sees only this interface, so unit tests can inject a fake;
 * createHeadlessMirror is the real one. Loaded by the shell host only —
 * nothing under ui/src imports @xterm/headless.
 */
export interface ShellMirror {
  /**
   * Queues data for parsing. xterm parses asynchronously, so `callback` runs
   * once everything written before this call has been parsed and nothing
   * written after it has — the only exact point to serialize or resize at.
   */
  write(data: string, callback?: () => void): void;
  resize(cols: number, rows: number): void;
  /** The screen, scrollback and modes as a string that rebuilds them when written to a terminal. */
  serialize(): string;
  dispose(): void;
}

export type CreateMirror = (cols: number, rows: number) => ShellMirror;

/**
 * Scrollback lines kept by every shell's mirror. The dock's xterm must use the
 * same value: a longer view scrollback would sit empty above the restored
 * lines, a shorter one would silently drop part of the snapshot.
 */
export const SHELL_SCROLLBACK = 1000;

export const createHeadlessMirror: CreateMirror = (cols, rows) => {
  const terminal = new Terminal({ cols, rows, scrollback: SHELL_SCROLLBACK, allowProposedApi: true });
  const serializer = new SerializeAddon();
  // The addon's typings name the full @xterm/xterm Terminal; it only touches
  // the buffer and mode APIs the headless Terminal shares.
  terminal.loadAddon(serializer as unknown as Parameters<Terminal["loadAddon"]>[0]);
  return {
    write: (data, callback) => terminal.write(data, callback),
    resize: (c, r) => terminal.resize(c, r),
    // Normal buffer plus scrollback, then the alternate buffer when a
    // full-screen program (vim, less) is on it, and the terminal modes
    // (bracketed paste, application cursor keys, mouse tracking, …).
    // Not restored: the window title, cursor shape/blink, images, and
    // scroll regions or tab stops a program set up. The resize that follows
    // a reattach makes full-screen programs repaint over any such gap.
    serialize: () =>
      serializer.serialize({ scrollback: SHELL_SCROLLBACK, excludeAltBuffer: false, excludeModes: false }),
    dispose: () => {
      serializer.dispose();
      terminal.dispose();
    },
  };
};
