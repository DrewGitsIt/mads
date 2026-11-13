import type { SessionEventSource } from "../../router/client";
import type { PtyBridge } from "../TerminalRenderer/ptyBridge";

/**
 * Why a background output stream stopped: "ended" — the process finished
 * and every byte was sent; "gone" — its output file was removed; "none" —
 * the call has no background output to show (not one, refused, or a
 * provider without it).
 */
export type BackgroundEndReason = "ended" | "gone" | "none";

/** A full terminal reset (RIS): what a "reset" frame writes, so the view drops output from the file that went away. */
const RESET = "\x1bc";

/**
 * One background call's output stream (RouterClient.streamToolOutput),
 * decoded for a terminal. Chunks arrive as base64 bytes and go through one
 * streaming TextDecoder, so a UTF-8 character split across chunks comes out
 * whole. Subscribes at construction and keeps everything until a reader
 * attaches — TerminalView subscribes only after its async spawn, and a
 * short file can be over by then.
 */
export class BackgroundOutputSource {
  private readonly decoder = new TextDecoder();
  private buffered = "";
  private dataListener: ((data: string) => void) | undefined;
  private readonly stateListeners = new Set<() => void>();
  private closed = false;
  /** Bytes of the file before what the stream started with; 0 when it started at the top. */
  trimmed = 0;
  /** Bytes of output received (after any reset). */
  received = 0;
  endReason: BackgroundEndReason | undefined;

  constructor(private readonly es: SessionEventSource) {
    es.addEventListener("message", this.onChunk);
    es.addEventListener("trimmed", this.onTrimmed);
    es.addEventListener("reset", this.onReset);
    es.addEventListener("end", this.onEnd);
  }

  private onChunk = (ev: MessageEvent<string>) => {
    const bytes = Uint8Array.from(atob(ev.data), (c) => c.charCodeAt(0));
    this.received += bytes.length;
    this.push(this.decoder.decode(bytes, { stream: true }));
    this.changed();
  };

  private onTrimmed = (ev: MessageEvent<string>) => {
    this.trimmed = Number(ev.data) || 0;
    this.changed();
  };

  private onReset = () => {
    this.decoder.decode(); // drop a partial character from the old file
    this.received = 0;
    this.trimmed = 0;
    this.push(RESET);
    this.changed();
  };

  private onEnd = (ev: MessageEvent<string>) => {
    const reason = ev.data;
    this.endReason = reason === "ended" || reason === "gone" ? reason : "none";
    this.push(this.decoder.decode()); // flush any trailing partial bytes
    this.close();
    this.changed();
  };

  private push(text: string) {
    if (!text) return;
    if (this.dataListener) this.dataListener(text);
    else this.buffered += text;
  }

  private changed() {
    for (const l of this.stateListeners) l();
  }

  /** The terminal's feed: everything buffered so far, then each new piece. One reader at a time. */
  onData(listener: (data: string) => void): () => void {
    this.dataListener = listener;
    if (this.buffered) {
      const text = this.buffered;
      this.buffered = "";
      listener(text);
    }
    return () => {
      if (this.dataListener === listener) this.dataListener = undefined;
    };
  }

  /** Fires when trimmed, received or endReason change. */
  onState(listener: () => void): () => void {
    this.stateListeners.add(listener);
    return () => this.stateListeners.delete(listener);
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.es.removeEventListener("message", this.onChunk);
    this.es.removeEventListener("trimmed", this.onTrimmed);
    this.es.removeEventListener("reset", this.onReset);
    this.es.removeEventListener("end", this.onEnd);
    this.es.close();
  }
}

const PTY_ID = "background-output";

/**
 * Adapts a read-only BackgroundOutputSource into the PtyBridge shape
 * TerminalView expects, like the pipeline's PipelineOutputBridge: no input
 * channel, so write/resize are no-ops. kill is one too: TerminalView kills
 * its bridge on every unmount, StrictMode's rehearsal one included, so the
 * stream belongs to whoever made the source (BackgroundOutputView), not to
 * the terminal.
 */
export class BackgroundOutputBridge implements PtyBridge {
  constructor(private readonly source: BackgroundOutputSource) {}

  async spawn(): Promise<string> {
    return PTY_ID;
  }

  write(): void {}

  resize(): void {}

  kill(): void {}

  onData(_ptyId: string, listener: (data: string) => void): () => void {
    return this.source.onData(listener);
  }

  onExit(): () => void {
    return () => {};
  }
}

/** "1.2 MB", "64 KB", "312 bytes": how much earlier output the view skipped. */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} bytes`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}
