import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyedRow } from "./conversation";
import {
  emptyReveal,
  pendingWordsAtLeast,
  revealAll as revealAllPure,
  revealedRows,
  revealPending,
  revealStep,
  type RevealState,
} from "./reveal";

// "Behind" — worth offering Show all — once this many seconds of reading
// wait. Below it the reveal is keeping up with the model.
const BEHIND_SECONDS = 2;
// Clamp per-frame dt so a stalled/backgrounded rAF doesn't dump a huge chunk
// on the next frame.
const MAX_FRAME_SECONDS = 0.1;

export interface Reveal {
  /** The rows to render: paced text cut to what's shown, rows after the gate held back. */
  rows: KeyedRow[];
  /** Paced text still waits to be shown. */
  pending: boolean;
  /** More than a couple of seconds of reading still waits: the reveal trails the agent. */
  behind: boolean;
  /**
   * Show everything now (Show all) — or only the rows `only` picks (the fast
   * finish when a new turn starts: the last turn's rows, not the new one's).
   */
  revealAll: (only?: (row: KeyedRow) => boolean) => void;
}

/**
 * Drives reveal.ts at `wordsPerMinute` off requestAnimationFrame, decoupling
 * the reading pace from token arrival. `null` is Native: nothing is held.
 * There is deliberately no catch-up: a pace the user chose holds even after
 * the agent finished, so a burst plays out at that pace. `final` (the turn is
 * over) reveals a row's last word whole. Reveals are batched once per frame,
 * which also spares a re-render per delta.
 */
export function useReveal(rows: KeyedRow[], wordsPerMinute: number | null, final: boolean): Reveal {
  const [state, setState] = useState<RevealState>(emptyReveal);
  const stateRef = useRef(state);
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const rateRef = useRef(wordsPerMinute);
  rateRef.current = wordsPerMinute;
  const finalRef = useRef(final);
  finalRef.current = final;
  const rafRef = useRef<number | null>(null);
  const lastTsRef = useRef<number | null>(null);
  const paced = wordsPerMinute != null && Number.isFinite(wordsPerMinute);

  const commit = useCallback((next: RevealState) => {
    if (next === stateRef.current) return;
    stateRef.current = next;
    setState(next);
  }, []);

  const stopLoop = useCallback(() => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    rafRef.current = null;
    lastTsRef.current = null;
  }, []);

  const frame = useCallback(
    (ts: number) => {
      const rate = rateRef.current;
      const rows = rowsRef.current;
      if (rate == null || !Number.isFinite(rate)) {
        commit(revealAllPure(rows, stateRef.current));
        rafRef.current = null;
        lastTsRef.current = null;
        return;
      }
      const last = lastTsRef.current ?? ts;
      const dt = Math.min(MAX_FRAME_SECONDS, (ts - last) / 1000);
      lastTsRef.current = ts;
      commit(revealStep(rows, stateRef.current, (rate / 60) * dt, finalRef.current));
      if (revealPending(rows, stateRef.current)) {
        rafRef.current = requestAnimationFrame(frame);
      } else {
        rafRef.current = null;
        lastTsRef.current = null;
      }
    },
    [commit],
  );

  // Native: everything shows as it arrives, and the reveal state keeps up so
  // a switch to a reading pace never re-hides text already read.
  useLayoutEffect(() => {
    if (!paced && revealPending(rows, stateRef.current)) commit(revealAllPure(rows, stateRef.current));
  }, [paced, rows, commit]);

  // Paced: run the loop while text waits.
  useEffect(() => {
    if (paced && rafRef.current === null && revealPending(rows, stateRef.current)) {
      lastTsRef.current = null;
      rafRef.current = requestAnimationFrame(frame);
    }
  }, [paced, rows, frame]);

  useEffect(() => stopLoop, [stopLoop]);

  const revealAll = useCallback(
    (only?: (row: KeyedRow) => boolean) => {
      stopLoop();
      const rows = only ? rowsRef.current.filter(only) : rowsRef.current;
      commit(revealAllPure(rows, stateRef.current));
      // Anything left (the new turn's text) carries on at the pace.
      if (revealPending(rowsRef.current, stateRef.current)) {
        rafRef.current = requestAnimationFrame(frame);
      }
    },
    [stopLoop, commit, frame],
  );

  return useMemo(() => {
    if (!paced) return { rows, pending: false, behind: false, revealAll };
    const behind = pendingWordsAtLeast(rows, state, Math.max(1, ((wordsPerMinute ?? 0) / 60) * BEHIND_SECONDS));
    return { rows: revealedRows(rows, state), pending: revealPending(rows, state), behind, revealAll };
  }, [paced, rows, state, wordsPerMinute, revealAll]);
}
