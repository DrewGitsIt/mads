import type { PtyBridge } from "../../conversation/TerminalRenderer/ptyBridge";
import type { PipelineOutputSource } from "../client/pipelineClient";

const PTY_ID = "pipeline-output";

/**
 * Adapts a read-only `PipelineOutputSource` into the `PtyBridge` shape
 * `TerminalView` expects (HANDOFF.md §6). `write`/`resize` are no-ops — a
 * pipeline stage's one-shot agent call has no interactive input channel.
 */
export class PipelineOutputBridge implements PtyBridge {
  constructor(private readonly source: PipelineOutputSource) {}

  async spawn(): Promise<string> {
    return PTY_ID;
  }

  write(): void {}

  resize(): void {}

  kill(): void {
    this.source.close();
  }

  onData(_ptyId: string, listener: (data: string) => void): () => void {
    return this.source.onChunk(listener);
  }

  onExit(): () => void {
    return () => {};
  }
}
