import { useCallback, useEffect, useRef, useState } from "react";
import { subscribeToEvents } from "../../../router/events";
import type { SessionAgentState } from "../rail/railGroups";
import type { Envelope, SubagentTask } from "../../../router/types";

/**
 * Phase 0 of the rail's status line (docs/mockups/sessions-ux-v2.html §1):
 * every signal here already rides the shared control-plane stream, so the
 * rail can watch all sessions at once on ONE connection — the per-session
 * data-plane /output stream costs one socket per card and the browser only
 * grants ~6 per origin.
 *
 * Known phase-0 gap (closed by the session.agent_state task): a live tool
 * title and "Thinking…" need wire data the control plane doesn't carry, so an
 * in-flight turn with no subagents reads as a generic "Working…"; and
 * session.turn_ended is skipped for a turn that produced no delta, so a no-op
 * turn can miss "Done · your turn".
 */
const AGENT_STATE_EVENT_TYPES = [
  "session.crashed",
  "session.permission_requested",
  "session.permission_resolved",
  "session.subagent_task",
  "session.turn_ended",
];

/** Mutable per-session accumulator. Kept out of state: only its projection renders. */
interface Fold {
  crashed: boolean;
  /** Unresolved permission parks, request_id → the tool it named and when it parked. */
  pending: Map<string, { tool?: string; at: string }>;
  /** Spawned subagents that haven't reported a final notification. */
  running: Set<string>;
  workingSince?: string;
  done: boolean;
}

function emptyFold(): Fold {
  return { crashed: false, pending: new Map(), running: new Set(), done: false };
}

/**
 * Any in-turn signal means a turn is in flight: it clears a stale crash (the
 * session plainly restarted) and the sticky "Done · your turn" (the next turn
 * started, which is one of that stickiness's two exits — the other is opening
 * the session).
 */
function markWorking(fold: Fold, at: string): void {
  fold.crashed = false;
  fold.done = false;
  fold.workingSince ??= at;
}

function apply(fold: Fold, env: Envelope): void {
  switch (env.type) {
    case "session.crashed":
      // A crash ends whatever was in flight — nothing is working, parked or
      // waiting on the user any more.
      fold.crashed = true;
      fold.done = false;
      fold.workingSince = undefined;
      fold.pending.clear();
      fold.running.clear();
      return;
    case "session.permission_requested": {
      const p = (env.payload ?? {}) as { request_id?: string; tool?: string };
      markWorking(fold, env.timestamp);
      fold.pending.set(p.request_id ?? String(env.seq), { tool: p.tool, at: env.timestamp });
      return;
    }
    case "session.permission_resolved": {
      const p = (env.payload ?? {}) as { request_id?: string };
      // Resolved either way, the turn carries on — the park is over, the work isn't.
      markWorking(fold, env.timestamp);
      fold.pending.delete(p.request_id ?? String(env.seq));
      return;
    }
    case "session.subagent_task": {
      const task = (env.payload ?? {}) as Partial<SubagentTask>;
      markWorking(fold, env.timestamp);
      if (!task.task_id) return;
      // Same vocabulary as the conversation's own fold (conversationItems.ts):
      // only a final "notification" retires a subagent.
      if (task.phase === "notification") fold.running.delete(task.task_id);
      else fold.running.add(task.task_id);
      return;
    }
    case "session.turn_ended":
      fold.crashed = false;
      fold.workingSince = undefined;
      fold.pending.clear();
      // A turn end with no notification leaves nothing running, the same
      // conclusion finishRunningTasks() draws in the conversation.
      fold.running.clear();
      fold.done = true;
      return;
  }
}

/** Only sessions with something to say appear, exactly like useOpenTaskCounts. */
function project(folds: Map<string, Fold>): Record<string, SessionAgentState> {
  const next: Record<string, SessionAgentState> = {};
  for (const [id, fold] of folds) {
    // The oldest unresolved park is the one the card names: it's the one that
    // has been waiting on the user longest.
    const park = [...fold.pending.values()].sort((a, b) => a.at.localeCompare(b.at))[0];
    const state: SessionAgentState = { subagents: fold.running.size };
    if (fold.crashed) state.crashed = true;
    if (park) {
      state.awaitingSince = park.at;
      if (park.tool) state.awaitingTool = park.tool;
    }
    if (fold.workingSince) state.workingSince = fold.workingSince;
    if (fold.done) state.done = true;
    if (state.crashed || state.awaitingSince || state.workingSince || state.done) next[id] = state;
  }
  return next;
}

/**
 * Per-session live agent state, folded from the control-plane session.*
 * stream — one map keyed by session id, the same shape (and the same shared
 * SSE bus) as useOpenTaskCounts. `openSessionId` is the sticky-done reader
 * receipt: opening a session is the user seeing that it came back.
 */
export function useSessionAgentState(openSessionId?: string): Record<string, SessionAgentState> {
  const folds = useRef(new Map<string, Fold>());
  const [states, setStates] = useState<Record<string, SessionAgentState>>({});
  // Read inside the subscriber, so changing the open session never tears down
  // and reopens the connection.
  const openRef = useRef(openSessionId);
  openRef.current = openSessionId;

  const publish = useCallback(() => setStates(project(folds.current)), []);

  useEffect(() => {
    const unsubscribe = subscribeToEvents({ types: AGENT_STATE_EVENT_TYPES }, (env) => {
      if (!env.session_id) return;
      let fold = folds.current.get(env.session_id);
      if (!fold) {
        fold = emptyFold();
        folds.current.set(env.session_id, fold);
      }
      apply(fold, env);
      // A turn that ends in the session you're looking at was never news.
      if (env.session_id === openRef.current) fold.done = false;
      publish();
    });
    return unsubscribe;
  }, [publish]);

  // Opening a session is the other exit from sticky done — you've seen it.
  useEffect(() => {
    if (!openSessionId) return;
    const fold = folds.current.get(openSessionId);
    if (!fold?.done) return;
    fold.done = false;
    publish();
  }, [openSessionId, publish]);

  return states;
}
