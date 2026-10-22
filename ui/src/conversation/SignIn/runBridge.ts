import type { PtyBridge, PtySpawnOptions, ShellHostBridge } from "../TerminalRenderer/ptyBridge";

/**
 * PtyBridge over the shell host's `run` request, for ONE command (a
 * provider's login, docs/AUTH-SIGNIN-DESIGN.md). Unlike the dock's
 * ShellBridge, the process belongs to the view: `spawn` starts the command
 * and `kill` (TerminalView's unmount) ends it. A command that already exited
 * is gone from the host, so ending it then is a harmless no-op.
 */
export class RunBridge implements PtyBridge {
  constructor(
    private readonly host: ShellHostBridge,
    private readonly argv: string[],
    private readonly env: string[] = [],
  ) {}

  async spawn(options?: PtySpawnOptions): Promise<string> {
    const { shellId } = await this.host.run(this.argv, { env: this.env, cols: options?.cols, rows: options?.rows });
    return shellId;
  }

  write(ptyId: string, data: string): void {
    this.host.write(ptyId, data);
  }

  resize(ptyId: string, cols: number, rows: number): void {
    this.host.resize(ptyId, cols, rows);
  }

  kill(ptyId: string): void {
    this.host.end(ptyId).catch(() => undefined);
  }

  onData(ptyId: string, listener: (data: string) => void): () => void {
    return this.host.onData(ptyId, listener);
  }

  onExit(ptyId: string, listener: (exitCode: number) => void): () => void {
    return this.host.onExit(ptyId, listener);
  }
}

/** The login as one line to paste into a terminal: env overrides first, each word quoted when it needs it. */
export function commandLine(argv: string[], env: string[] = []): string {
  return [...env.map(quoteEnv), ...argv.map(quote)].join(" ");
}

function quote(word: string): string {
  return /^[\w@%+=:,./-]+$/.test(word) ? word : `'${word.replace(/'/g, `'\\''`)}'`;
}

function quoteEnv(pair: string): string {
  const eq = pair.indexOf("=");
  return eq > 0 ? `${pair.slice(0, eq)}=${quote(pair.slice(eq + 1))}` : quote(pair);
}
