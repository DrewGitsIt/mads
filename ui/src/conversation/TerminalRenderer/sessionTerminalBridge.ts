import type { RouterClient, SessionEventSource } from "../../router/client";
import type { StreamEvent } from "../../router/types";
import type { PtyBridge } from "./ptyBridge";

/**
 * Adapts the session's real backend transport into the `PtyBridge` shape
 * `TerminalView` expects (HANDOFF.md), so the Conversation tab's Terminal
 * display mode drives the agent's *own* provider process instead of
 * Electron's `ptyBridge` (that prop is now `ShellDock`-only). Direct prior
 * art: `pipeline/DrillIn/pipelineOutputBridge.ts`'s `PipelineOutputBridge`,
 * but read/write instead of read-only.
 */
export class SessionTerminalBridge implements PtyBridge {
  constructor(
    private readonly client: RouterClient,
    private readonly sessionId: string,
    private readonly eventSource: SessionEventSource, // already subscribed via streamSession()
  ) {}

  async spawn(): Promise<string> {
    return this.sessionId; // one pty id per session, not per mount
  }

  write(_ptyId: string, data: string): void {
    void this.client.terminalWrite(this.sessionId, data);
  }

  resize(_ptyId: string, cols: number, rows: number): void {
    void this.client.terminalResize(this.sessionId, cols, rows);
  }

  // Switching away is a transport switch (SwitchTransport, server-side),
  // not a kill — TerminalView calls this on unmount, but unmounting the
  // terminal *view* must never kill the underlying provider process.
  kill(): void {}

  onData(_ptyId: string, listener: (data: string) => void): () => void {
    const handler = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind === "pty_output") listener(parsed.text);
    };
    this.eventSource.addEventListener("pty_output", handler);
    return () => this.eventSource.removeEventListener("pty_output", handler);
  }

  onExit(): () => void {
    return () => {};
  }
}
