import type { FakeRouterClient, FakeSessionEventSource } from "../../router/fixtures";
import type { StreamEvent, ToolUse, TranscriptMessage, TranscriptPage } from "../../router/types";

/**
 * Test-only: drives a FakeSessionEventSource the way the session child's
 * keyed stream frames a turn (docs/CHAT-STREAM-DESIGN.md §2): an item_start
 * per new key, deltas under it, item_stop when a text run ends, a tool's call
 * and result under one key, every numbered frame with an `<epoch>:<seq>` id.
 * Keys are minted here (`a<n>`, `th<n>`, `tool:<id>`, `user:<n>`).
 */
export class FrameDriver {
  private seq = 0;
  private n = 0;
  /** The open text run, per parent ("" = the main agent). */
  private openText = new Map<string, string>();

  constructor(
    readonly source: FakeSessionEventSource,
    readonly epoch = "e0",
  ) {}

  /** One numbered frame. */
  frame(kind: string, data: Record<string, unknown> = {}): void {
    this.source.emitFrame(kind, data, `${this.epoch}:${++this.seq}`);
  }

  /** The connection's first frame (no id). */
  hello(extra: Record<string, unknown> = {}): void {
    this.source.emitFrame("hello", {
      epoch: this.epoch,
      seq: this.seq,
      keyed: true,
      resume: "fresh",
      turn: { active: false },
      queued: [],
      ...extra,
    });
  }

  replayEnd(): void {
    this.frame("replay_end");
  }

  /** A user_message; returns its key. */
  user(text: string, extra: Record<string, unknown> = {}): string {
    const key = (extra.key as string | undefined) ?? `user:${++this.n}`;
    this.frame("user_message", { key, text, ts: new Date().toISOString(), ...extra });
    return key;
  }

  /** A text delta, continuing the open run or starting a new item; returns its key. */
  text(text: string, parent?: string): string {
    const p = parent ?? "";
    let key = this.openText.get(p);
    if (!key) {
      key = `a${++this.n}`;
      this.openText.set(p, key);
      this.frame("item_start", { key, item: "text", ...(parent ? { parent_tool_use_id: parent } : {}) });
    }
    this.frame("text_delta", { key, text });
    return key;
  }

  /** Ends the open text run (a tool or thinking starts, or the turn ends). */
  stopText(parent?: string): void {
    const p = parent ?? "";
    const key = this.openText.get(p);
    if (!key) return;
    this.openText.delete(p);
    this.frame("item_stop", { key });
  }

  /** A whole thinking item; returns its key. */
  thinking(text: string, parent?: string): string {
    this.stopText(parent);
    const key = `th${++this.n}`;
    this.frame("item_start", { key, item: "thinking", ...(parent ? { parent_tool_use_id: parent } : {}) });
    this.frame("thinking_delta", { key, text });
    this.frame("item_stop", { key });
    return key;
  }

  /** A tool call (or a repeat: a status update); returns its key. */
  tool(tool: ToolUse, parent?: string): string {
    this.stopText(parent);
    const key = `tool:${tool.id ?? ++this.n}`;
    this.frame("item_start", { key, item: "tool_use", ...(parent ? { parent_tool_use_id: parent } : {}) });
    this.frame("tool_use", { key, tool });
    return key;
  }

  toolResult(id: string | undefined, text?: string, isError?: boolean, tool?: ToolUse): void {
    this.frame("tool_result", { key: `tool:${id}`, text, is_error: !!isError, tool: tool ?? { id, name: "" } });
  }

  /** A status frame (today's provider.Event data). */
  status(kind: string, data: Record<string, unknown> = {}): void {
    this.frame(kind, data);
  }

  turnEnd(data: Record<string, unknown> = {}): void {
    for (const p of [...this.openText.keys()]) this.stopText(p || undefined);
    this.frame("turn_end", data);
  }

  settled(keys: string[]): void {
    this.frame("settled", { keys });
  }

  unsaved(keys: string[]): void {
    this.frame("unsaved", { keys });
  }

  /**
   * A legacy-shaped StreamEvent as the keyed stream frames it: text, tool
   * calls and results, thinking (a block becomes an item, a bare token count
   * a status tick), inbound; anything else is a status frame.
   */
  emit(ev: StreamEvent): void {
    const parent = "parent_tool_use_id" in ev ? (ev.parent_tool_use_id ?? undefined) : undefined;
    switch (ev.kind) {
      case "text":
        this.text(ev.text, parent);
        return;
      case "tool_use":
        this.tool(ev.tool, parent);
        return;
      case "tool_result":
        this.toolResult(ev.tool?.id, ev.text, ev.is_error, ev.tool);
        return;
      case "thinking":
        if (ev.text) this.thinking(ev.text, parent);
        else this.status("thinking", { ...ev });
        return;
      case "inbound": {
        this.stopText();
        this.frame("inbound", { ...ev, key: `in${++this.n}` });
        return;
      }
      case "turn_end":
        this.turnEnd({ ...ev });
        return;
      default:
        this.status(ev.kind, { ...ev });
    }
  }
}

const drivers = new WeakMap<FakeSessionEventSource, FrameDriver>();

/** The driver for a client's latest (or `index`th) keyed stream: one per source. */
export function frames(client: FakeRouterClient, index = -1): FrameDriver {
  const entry = client.v2Sources.at(index);
  if (!entry) throw new Error("no /output?v=2 stream opened yet");
  let d = drivers.get(entry.source);
  if (!d) drivers.set(entry.source, (d = new FrameDriver(entry.source)));
  return d;
}

/** Test-only: one `/transcript?v=2` page. */
export function page(rows: TranscriptMessage[], hasMore = false): TranscriptPage {
  return { rows, has_more: hasMore };
}
