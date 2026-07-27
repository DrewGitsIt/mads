import { useCallback, useEffect, useRef, useState } from "react";
import type { ToolUse } from "../../router/types";
import type { StreamItem } from "../ChatRenderer/ChatRenderer";
import {
  applyToolResult as applyToolResultPure,
  drainAll,
  emptyPacer,
  enqueueText as enqueueTextPure,
  enqueueTool as enqueueToolPure,
  hasPending,
  pendingChars,
  step,
  type PacerState,
} from "./streamPacer";

// The reveal never lags the model by more than this — beyond it, the rate is
// bumped so a long fast response doesn't keep "typing" for tens of seconds
// after the model already finished.
const MAX_LAG_SECONDS = 1;
// Clamp per-frame dt so a stalled/backgrounded rAF doesn't dump a huge chunk on
// the next frame.
const MAX_FRAME_SECONDS = 0.1;

export interface StreamPacer {
  /** Revealed items to render for the live turn. */
  items: StreamItem[];
  enqueueText: (text: string) => void;
  enqueueTool: (tool: ToolUse) => void;
  applyToolResult: (id: string | undefined, result: { text?: string; is_error?: boolean }) => void;
  /** Keep draining what's queued, then invoke `onDrained` (used at turn_end). */
  finish: (onDrained: () => void) => void;
  /** Clear everything (session switch, turn reset). */
  reset: () => void;
}

/**
 * Drives a {@link PacerState} at `charsPerSecond` off requestAnimationFrame,
 * decoupling render speed from token arrival. `null` = passthrough (reveal
 * immediately, today's behavior). Reveals are batched once per frame, which
 * also removes the per-token re-render thrash of updating on every delta.
 */
export function useStreamPacer(charsPerSecond: number | null): StreamPacer {
  const [items, setItems] = useState<StreamItem[]>([]);
  const stateRef = useRef<PacerState>(emptyPacer());
  const rafRef = useRef<number | null>(null);
  const lastTsRef = useRef<number | null>(null);
  const finishingRef = useRef<(() => void) | null>(null);
  const rateRef = useRef(charsPerSecond);
  rateRef.current = charsPerSecond;

  const commit = useCallback((next: PacerState) => {
    stateRef.current = next;
    setItems(next.items);
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
      const backlogRate = pendingChars(stateRef.current) / MAX_LAG_SECONDS;
      const chars = Math.max(rate, backlogRate) * dt;
      commit(step(stateRef.current, chars));
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
    (tool: ToolUse) => {
      if (rateRef.current == null || !Number.isFinite(rateRef.current)) {
        commit(drainAll(enqueueToolPure(stateRef.current, tool)));
        return;
      }
      stateRef.current = enqueueToolPure(stateRef.current, tool);
      ensureLoop();
    },
    [commit, ensureLoop],
  );

  const applyToolResult = useCallback(
    (id: string | undefined, result: { text?: string; is_error?: boolean }) => {
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

  const reset = useCallback(() => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    rafRef.current = null;
    lastTsRef.current = null;
    finishingRef.current = null;
    stateRef.current = emptyPacer();
    setItems([]);
  }, []);

  useEffect(() => {
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, []);

  return { items, enqueueText, enqueueTool, applyToolResult, finish, reset };
}
