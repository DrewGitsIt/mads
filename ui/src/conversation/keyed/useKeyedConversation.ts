import { useCallback, useEffect, useMemo, useReducer, useRef } from "react";
import type { RouterClient, SessionEventSource } from "../../router/client";
import {
  conversationReducer,
  emptyConversation,
  orderedRows,
  type ConvAction,
  type ConvState,
  type Frame,
  type KeyedRow,
} from "./conversation";

/** Rows per transcript page: the mount, the turn-end refresh, each scroll-up. */
export const PAGE_LIMIT = 100;

/** Frames the conversation itself is built from (docs/CHAT-STREAM-DESIGN.md §2). */
const CHAT_KINDS = [
  "hello",
  "user_message",
  "item_start",
  "text_delta",
  "thinking_delta",
  "item_stop",
  "tool_use",
  "tool_result",
  "notice",
  "inbound",
  "settled",
  "unsaved",
  "tail_truncated",
  "resync",
  "replay_end",
] as const;

/**
 * Status frames: today's provider.Event data, numbered like the rest. The
 * conversation tracks turn_end itself; every one also goes to `onStatus`.
 */
export const STATUS_KINDS = [
  "turn_end",
  "error",
  "permission_request",
  "task",
  "task_roster",
  "background",
  "compact",
  "account",
  "subagent_usage",
  "thinking",
] as const;

export interface KeyedConversation {
  state: ConvState;
  /** The main list in render order (subagent rows excluded). */
  rows: KeyedRow[];
  dispatch: (a: ConvAction) => void;
  /** Fetch the page before the oldest loaded row; resolves once merged. */
  loadOlder: () => Promise<void>;
  /** Refetch the last page and merge it by key (turn end, a terminal-mode turn). */
  refreshLast: () => Promise<void>;
  /** Close the stream and open it again from the last frame seen. */
  reattach: () => void;
  /** The mount fetch failed (the transcript, not the stream). */
  error?: unknown;
}

export interface KeyedConversationOptions {
  /** Open `/output?v=2`. False: transcript pages only (a terminal-mode session's chat view). */
  stream: boolean;
  /** Every status frame, and an inbound frame without a key, as today's handlers take them. */
  onStatus?: (kind: string, data: Record<string, unknown>) => void;
  /** The mount page failed. */
  onLoadError?: (err: unknown) => void;
}

/**
 * The keyed conversation for one session: mounts with the last page of
 * `/transcript?v=2` and `/output?v=2` (§5), merges by key, and replaces the
 * old wholesale refetches. A resync or an evicted tail fetches the page
 * first and swaps it in as one reducer action, holding the frames that
 * arrive meanwhile, so the screen never shows the gap.
 */
export function useKeyedConversation(
  client: RouterClient,
  sessionId: string,
  opts: KeyedConversationOptions,
): KeyedConversation {
  const [state, dispatch] = useReducer(conversationReducer, undefined, emptyConversation);
  const stateRef = useRef(state);
  stateRef.current = state;
  const onStatusRef = useRef(opts.onStatus);
  onStatusRef.current = opts.onStatus;
  const onLoadErrorRef = useRef(opts.onLoadError);
  onLoadErrorRef.current = opts.onLoadError;
  const errorRef = useRef<unknown>(undefined);
  const [generation, bump] = useReducer((n: number) => n + 1, 0);
  const olderInFlight = useRef<Promise<void> | null>(null);
  const streamedSession = useRef<string | null>(null);

  const fetchLast = useCallback(
    () => client.getTranscriptPage(sessionId, { limit: PAGE_LIMIT }),
    [client, sessionId],
  );

  const refreshLast = useCallback(async () => {
    const page = await fetchLast();
    dispatch({ type: "page", rows: page.rows ?? [], hasMore: page.has_more });
  }, [fetchLast]);

  // Mount: the last page. A new session id starts over.
  useEffect(() => {
    let cancelled = false;
    dispatch({ type: "reset" });
    errorRef.current = undefined;
    fetchLast().then(
      (page) => {
        if (!cancelled) dispatch({ type: "page", rows: page.rows ?? [], hasMore: page.has_more });
      },
      (err) => {
        if (cancelled) return;
        errorRef.current = err;
        onLoadErrorRef.current?.(err);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [fetchLast]);

  // The stream. Reopened with the last frame id on reattach; the browser's
  // own reconnect sends Last-Event-ID itself.
  useEffect(() => {
    if (!opts.stream) return;
    let closed = false;
    // Reopening this session's stream resumes after the last frame seen; a
    // new session's starts fresh (its state was just reset).
    const after = streamedSession.current === sessionId ? stateRef.current.lastId : undefined;
    streamedSession.current = sessionId;
    const source: SessionEventSource = client.streamSessionV2(sessionId, after);
    // Frames held while a resync/eviction page is in flight, applied right
    // after the swap so the replay that follows lands on the new base.
    let held: Frame[] | null = null;

    const swap = (drop: "live" | string[]) => {
      held = held ?? [];
      fetchLast().then(
        (page) => {
          if (closed) return;
          const pending = held ?? [];
          held = null;
          dispatch({ type: "swap", rows: page.rows ?? [], hasMore: page.has_more, drop });
          for (const f of pending) dispatch({ type: "frame", frame: f });
        },
        () => {
          // Keep what's on screen; apply what arrived. The next turn end refreshes.
          if (closed) return;
          const pending = held ?? [];
          held = null;
          for (const f of pending) dispatch({ type: "frame", frame: f });
        },
      );
    };

    const handle = (kind: string) => (ev: MessageEvent<string>) => {
      if (closed || typeof ev.data !== "string") return; // a connection error carries no data
      let data: Record<string, unknown>;
      try {
        data = JSON.parse(ev.data) as Record<string, unknown>;
      } catch {
        return;
      }
      const frame: Frame = { kind, data, id: ev.lastEventId || undefined };
      const isStatus = (STATUS_KINDS as readonly string[]).includes(kind);
      if (isStatus || (kind === "inbound" && !data.key)) onStatusRef.current?.(kind, data);
      if (kind === "inbound" && !data.key) return;
      if (kind === "resync") {
        swap("live");
        return;
      }
      if (kind === "tail_truncated") {
        swap(((data.keys as string[] | undefined) ?? []).slice());
        return;
      }
      if (held) {
        held.push(frame);
        return;
      }
      dispatch({ type: "frame", frame });
      if (kind === "turn_end" && stateRef.current.keyed) {
        // One merge for timestamps and full tool results; no visible change.
        refreshLast().catch(() => {});
      }
    };

    const listeners: [string, (ev: MessageEvent<string>) => void][] = [...CHAT_KINDS, ...STATUS_KINDS].map((k) => [
      k,
      handle(k),
    ]);
    for (const [k, l] of listeners) source.addEventListener(k, l);
    return () => {
      closed = true;
      for (const [k, l] of listeners) source.removeEventListener(k, l);
      source.close();
    };
    // `generation` reopens the stream (reattach).
  }, [client, sessionId, opts.stream, fetchLast, refreshLast, generation]);

  const loadOlder = useCallback(async () => {
    const s = stateRef.current;
    if (!s.hasMore || !s.cursor) return;
    if (olderInFlight.current) return olderInFlight.current;
    const before = s.cursor;
    const p = client
      .getTranscriptPage(sessionId, { before, limit: PAGE_LIMIT })
      .then((page) => {
        if (stateRef.current.cursor === before) {
          dispatch({ type: "olderPage", rows: page.rows ?? [], hasMore: page.has_more });
        }
      })
      .finally(() => {
        olderInFlight.current = null;
      });
    olderInFlight.current = p;
    return p;
  }, [client, sessionId]);

  const rows = useMemo(() => orderedRows(state), [state]);
  return { state, rows, dispatch, loadOlder, refreshLast, reattach: bump, error: errorRef.current };
}
