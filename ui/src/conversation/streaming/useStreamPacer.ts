import { useCallback, useEffect, useRef, useState } from "react";
import type { InboundSource, ToolUse } from "../../router/types";
import type { StreamItem } from "../ChatRenderer/ChatRenderer";
import {
  applyToolResult as applyToolResultPure,
  drainAll,
  emptyPacer,
  enqueueInbound as enqueueInboundPure,
  enqueueText as enqueueTextPure,
  enqueueThinking as enqueueThinkingPure,
  enqueueTool as enqueueToolPure,
  hasPending,
  pendingWordsAtLeast,
  step,
  type PacerState,
} from "./streamPacer";

// "Behind" — worth offering Show all — once this many seconds of reading
// wait in the queue. Below it the reveal is keeping up with the model.
const BEHIND_SECONDS = 2;
// Clamp per-frame dt so a stalled/backgrounded rAF doesn't dump a huge chunk on
// the next frame.
const MAX_FRAME_SECONDS = 0.1;

export interface StreamPacer {
  /** Revealed items to render for the live turn. */
  items: StreamItem[];
  enqueueText: (text: string) => void;
  enqueueTool: (tool: ToolUse, timestamp?: string) => void;
  enqueueThinking: (text: string) => void;
  enqueueInbound: (text: string, inbound: InboundSource, timestamp?: string) => void;
  applyToolResult: (
    id: string | undefined,
    result: { text?: string; is_error?: boolean; timestamp?: string },
  ) => void;
  /** More than a couple of seconds of reading still queued — the reveal trails the agent. */
  behind: boolean;
  /** Keep draining what's queued, then invoke `onDrained` (used at turn_end). */
  finish: (onDrained: () => void) => void;
  /** Reveal everything queued now (Show all); a pending `finish` fires. */
  revealAll: () => void;
  /**
   * Reveal everything, return the items and clear — without firing a
   * pending `finish`. For handing a still-revealing turn over to the
   * history when the user sends their next message mid-reveal.
   */
  flush: () => StreamItem[];
  /** Clear everything (session switch, turn reset). */
  reset: () => void;
}

/**
 * Drives a {@link PacerState} at `wordsPerMinute` off requestAnimationFrame,
 * decoupling render speed from token arrival. `null` = passthrough (reveal
 * immediately — Native). There is deliberately no catch-up: a reading pace
 * the user chose holds even when the agent finished long ago, so a burst
 * plays out at that pace rather than flushing. Reveals are batched once per
 * frame, which also removes the per-token re-render thrash of updating on
 * every delta.
 */
export function useStreamPacer(wordsPerMinute: number | null): StreamPacer {
  const [items, setItems] = useState<StreamItem[]>([]);
  const [behind, setBehind] = useState(false);
  const stateRef = useRef<PacerState>(emptyPacer());
  const rafRef = useRef<number | null>(null);
  const lastTsRef = useRef<number | null>(null);
  const finishingRef = useRef<(() => void) | null>(null);
  const rateRef = useRef(wordsPerMinute);
  rateRef.current = wordsPerMinute;

  const commit = useCallback((next: PacerState) => {
    stateRef.current = next;
    setItems(next.items);
    const rate = rateRef.current;
    setBehind(
      rate != null && Number.isFinite(rate) && pendingWordsAtLeast(next, Math.max(1, (rate / 60) * BEHIND_SECONDS)),
    );
  }, []);

  const maybeFinish = useCallback(() => {
    if (finishingRef.current && !hasPending(stateRef.current)) {
      const cb = finishingRef.current;
      finishingRef.current = null;
      cb();
    }
  }, []);

  const frame = useCallback(
    (ts: number) => {
      const rate = rateRef.current;
      if (rate == null || !Number.isFinite(rate)) {
        commit(drainAll(stateRef.current));
        rafRef.current = null;
        lastTsRef.current = null;
        maybeFinish();
        return;
      }
      const last = lastTsRef.current ?? ts;
      const dt = Math.min(MAX_FRAME_SECONDS, (ts - last) / 1000);
      lastTsRef.current = ts;
      commit(step(stateRef.current, (rate / 60) * dt, finishingRef.current !== null));
      if (hasPending(stateRef.current)) {
        rafRef.current = requestAnimationFrame(frame);
      } else {
        rafRef.current = null;
        lastTsRef.current = null;
        maybeFinish();
      }
    },
    [commit, maybeFinish],
  );

  const ensureLoop = useCallback(() => {
    if (rafRef.current === null) {
      lastTsRef.current = null;
      rafRef.current = requestAnimationFrame(frame);
    }
  }, [frame]);

  const enqueueText = useCallback(
    (text: string) => {
      if (rateRef.current == null || !Number.isFinite(rateRef.current)) {
        commit(drainAll(enqueueTextPure(stateRef.current, text)));
        return;
      }
      stateRef.current = enqueueTextPure(stateRef.current, text);
      ensureLoop();
    },
    [commit, ensureLoop],
  );

  const enqueueTool = useCallback(
    (tool: ToolUse, timestamp?: string) => {
      if (rateRef.current == null || !Number.isFinite(rateRef.current)) {
        commit(drainAll(enqueueToolPure(stateRef.current, tool, timestamp)));
        return;
      }
      stateRef.current = enqueueToolPure(stateRef.current, tool, timestamp);
      ensureLoop();
    },
    [commit, ensureLoop],
  );

  const enqueueInbound = useCallback(
    (text: string, inbound: InboundSource, timestamp?: string) => {
      if (rateRef.current == null || !Number.isFinite(rateRef.current)) {
        commit(drainAll(enqueueInboundPure(stateRef.current, text, inbound, timestamp)));
        return;
      }
      stateRef.current = enqueueInboundPure(stateRef.current, text, inbound, timestamp);
      ensureLoop();
    },
    [commit, ensureLoop],
  );

  const enqueueThinking = useCallback(
    (text: string) => {
      if (rateRef.current == null || !Number.isFinite(rateRef.current)) {
        commit(drainAll(enqueueThinkingPure(stateRef.current, text)));
        return;
      }
      stateRef.current = enqueueThinkingPure(stateRef.current, text);
      ensureLoop();
    },
    [commit, ensureLoop],
  );

  const applyToolResult = useCallback(
    (id: string | undefined, result: { text?: string; is_error?: boolean; timestamp?: string }) => {
      commit(applyToolResultPure(stateRef.current, id, result));
    },
    [commit],
  );

  const finish = useCallback(
    (onDrained: () => void) => {
      if (!hasPending(stateRef.current)) {
        onDrained();
        return;
      }
      finishingRef.current = onDrained;
      ensureLoop();
    },
    [ensureLoop],
  );

  const stopLoop = useCallback(() => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    rafRef.current = null;
    lastTsRef.current = null;
  }, []);

  const revealAll = useCallback(() => {
    stopLoop();
    commit(drainAll(stateRef.current));
    maybeFinish();
  }, [stopLoop, commit, maybeFinish]);

  const reset = useCallback(() => {
    stopLoop();
    finishingRef.current = null;
    stateRef.current = emptyPacer();
    setItems([]);
    setBehind(false);
  }, [stopLoop]);

  const flush = useCallback(() => {
    const revealed = drainAll(stateRef.current).items;
    reset();
    return revealed;
  }, [reset]);

  useEffect(() => {
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, []);

  return {
    items,
    behind,
    enqueueText,
    enqueueTool,
    enqueueThinking,
    enqueueInbound,
    applyToolResult,
    finish,
    revealAll,
    flush,
    reset,
  };
}
