import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type { SessionAgentState } from "../rail/railGroups";
import type { AgentState, Delegate, Envelope, Session } from "../../../router/types";

/**
 * The rail's status line (docs/mockups/sessions-ux-v2.html §1): every signal
 * here rides the shared control-plane stream, so the rail can watch all
 * sessions at once on ONE connection — the per-session data-plane /output
 * stream costs one socket per card and the browser only grants ~6 per origin.
 *
 * session.agent_state is the authoritative turn signal (state, the live clause
 * and the turn's start), so working/idle is no longer inferred from whichever
 * in-turn event happened to arrive: the others each answer for their own field
 * only — a crash, an approval park, the running delegates. It also replaces
 * session.turn_ended as the end-of-turn signal, which is what closes the
 * "Done · your turn" gap on a turn that produced no digest delta (turn_ended
 * is skipped for those; a state transition is not).
 */
const AGENT_STATE_EVENT_TYPES = [
  "session.agent_state",
  "session.activity_changed",
  "session.crashed",
  "session.permission_requested",
  "session.permission_resolved",
  "session.delegate_changed",
];

/** Mutable per-session accumulator. Kept out of state: only its projection renders. */
interface Fold {
  crashed: boolean;
  /** Unresolved permission parks, request_id → the tool it named and when it parked. */
  pending: Map<string, { tool?: string; at: string }>;
  /**
   * Running delegates (docs/DELEGATES-DESIGN.md) by harness id — the server
   * decides their lifecycle, so this only mirrors session.delegate_changed.
   * `backgrounded`: the main agent is NOT waiting on it.
   */
  delegates: Map<string, { backgrounded: boolean; startedAt: string }>;
  /**
   * Every delegate this connection has heard about live, running or not. The
   * cold read (GET /delegates?state=running) may answer after a newer event,
   * and must never resurrect a delegate that event already retired.
   */
  heard: Set<string>;
  /**
   * The latest session.agent_state this connection saw. Once set it supersedes
   * the record mirror for good: the stream is newer than any list refetch.
   */
  live?: AgentState;
  done: boolean;
}

function emptyFold(): Fold {
  return {
    crashed: false,
    pending: new Map(),
    delegates: new Map(),
    heard: new Set(),
    done: false,
  };
}

/**
 * An in-turn signal clears the two stale states: a crash (the session plainly
 * restarted) and the sticky "Done · your turn" (the next turn started, which
 * is one of that stickiness's two exits — the other is opening the session).
 * It no longer decides that a turn is in flight; session.agent_state does.
 */
function clearStale(fold: Fold): void {
  fold.crashed = false;
  fold.done = false;
}

/**
 * The session's process is gone (asleep, or crashed): nothing is parked and
 * no delegate can still be running inside it. The server concludes the same
 * (the runner stops them on process exit), but a runner that died hard never
 * gets to say so — this is what keeps such a card from spinning forever.
 */
function clearInFlight(fold: Fold): void {
  fold.pending.clear();
  fold.delegates.clear();
}

function apply(fold: Fold, env: Envelope): void {
  switch (env.type) {
    case "session.agent_state": {
      const state = (env.payload ?? {}) as Partial<AgentState>;
      if (state.state !== "working" && state.state !== "idle") return;
      // `since` is the turn's start, held across the turn's label changes so
      // the card's elapsed counter spans the whole turn. Fall back to the
      // envelope's own stamp rather than dropping the transition.
      fold.live = { state: state.state, label: state.label, since: state.since || env.timestamp };
      if (state.state === "working") {
        clearStale(fold);
        return;
      }
      fold.crashed = false;
      // The main agent's turn is over, so nothing of ITS is parked. Its
      // delegates are another matter: a backgrounded one outlives the turn
      // that spawned it, and only the server's delegate record says when it
      // ends — retiring it here is what used to read a busy session "Ready".
      fold.pending.clear();
      fold.done = true;
      return;
    }
    case "session.activity_changed": {
      // Going to sleep is the turn's other ending, and the only one that
      // arrives when the child is killed rather than finished — a rebuild, a
      // Close, the supervisor healing a stale `active` record. Without it a
      // session terminated mid-sidechain keeps both a spinning card and its
      // subagent count forever: mirroredWorking() refuses to trust a working
      // mirror on a dead session, but `live` outranks the mirror, so the
      // guard never gets a say. Waking says nothing about a turn, so only
      // the transition to inactive is news here.
      const p = (env.payload ?? {}) as { activity?: string };
      if (p.activity !== "inactive") return;
      fold.live = { state: "idle", since: env.timestamp };
      clearInFlight(fold);
      return;
    }
    case "session.crashed":
      // A crash ends whatever was in flight — nothing is working, parked or
      // waiting on the user any more. The idle marker is deliberate: it stops
      // a stale working mirror on the record from re-animating the card.
      fold.crashed = true;
      fold.done = false;
      fold.live = { state: "idle", since: env.timestamp };
      clearInFlight(fold);
      return;
    case "session.permission_requested": {
      const p = (env.payload ?? {}) as { request_id?: string; tool?: string };
      clearStale(fold);
      fold.pending.set(p.request_id ?? String(env.seq), { tool: p.tool, at: env.timestamp });
      return;
    }
    case "session.permission_resolved": {
      const p = (env.payload ?? {}) as { request_id?: string };
      // Resolved either way, the turn carries on — the park is over, the work isn't.
      clearStale(fold);
      fold.pending.delete(p.request_id ?? String(env.seq));
      return;
    }
    case "session.delegate_changed": {
      const d = (env.payload ?? {}) as Partial<Delegate>;
      // Not an in-turn signal: a delegate reports long after the turn that
      // spawned it, and must not retire a sticky "Done · your turn".
      if (!d.harness_id) return;
      fold.heard.add(d.harness_id);
      if (d.state === "running") {
        fold.delegates.set(d.harness_id, {
          backgrounded: Boolean(d.backgrounded),
          startedAt: d.started_at ?? env.timestamp,
        });
      } else {
        fold.delegates.delete(d.harness_id);
      }
      return;
    }
  }
}

/**
 * The record mirror, read as the state this browser would otherwise have
 * missed — a reload or an SSE reconnect mid-turn has only the record to go on,
 * which is exactly why the Runner writes it.
 *
 * Read as idle unless the session is live: only the running Runner clears
 * agent_state (pkg/contracts/session.go says so), so a hard-killed process
 * leaves a working state behind and a card would spin forever.
 */
function mirroredWorking(session: Session): AgentState | undefined {
  const state = session.agent_state;
  if (!state || state.state !== "working" || session.activity !== "active") return undefined;
  return state;
}

/** Only sessions with something to say appear, exactly like useOpenTaskCounts. */
function project(
  folds: Map<string, Fold>,
  sessions: Session[],
): Record<string, SessionAgentState> {
  const next: Record<string, SessionAgentState> = {};
  const ids = new Set(folds.keys());
  const byId = new Map<string, Session>();
  for (const session of sessions) {
    byId.set(session.id, session);
    if (mirroredWorking(session)) ids.add(session.id);
  }
  for (const id of ids) {
    const fold = folds.get(id);
    const session = byId.get(id);
    // The stream wins wherever it has spoken — including when it says idle.
    // The record only fills the silence before this connection's first
    // transition for that session.
    const agent = fold?.live ?? (session ? mirroredWorking(session) : undefined);
    // The oldest unresolved park is the one the card names: it's the one that
    // has been waiting on the user longest.
    const park = [...(fold?.pending.values() ?? [])].sort((a, b) => a.at.localeCompare(b.at))[0];
    // Delegates count only on a live session: a sleeping one's process is
    // gone, whatever a record that missed its last write still says.
    const delegates =
      fold && (!session || session.activity === "active") ? [...fold.delegates.values()] : [];
    const state: SessionAgentState = { subagents: delegates.length };
    // Only when it says something, like every other field here: absent means
    // "they all block", so the common case keeps the shape it always had.
    const blocking = delegates.filter((d) => !d.backgrounded).length;
    if (blocking !== state.subagents) state.blockingSubagents = blocking;
    if (delegates.length > 0) {
      state.subagentsSince = delegates.map((d) => d.startedAt).sort()[0];
    }
    if (fold?.crashed) state.crashed = true;
    if (park) {
      state.awaitingSince = park.at;
      if (park.tool) state.awaitingTool = park.tool;
    }
    if (agent?.state === "working") {
      state.workingSince = agent.since;
      if (agent.label) state.label = agent.label;
    }
    if (fold?.done) state.done = true;
    if (state.crashed || state.awaitingSince || state.workingSince || state.done || state.subagents > 0) {
      next[id] = state;
    }
  }
  return next;
}

/**
 * Per-session live agent state, folded from the control-plane session.*
 * stream over the mirror on each session record — one map keyed by session id,
 * the same shape (and the same shared SSE bus) as useOpenTaskCounts.
 *
 * `sessions` supplies that mirror, so a reload mid-turn paints the live state
 * from the list fetch alone rather than waiting for the next transition.
 * `openSessionId` is the sticky-done reader receipt: opening a session is the
 * user seeing that it came back.
 */
export function useSessionAgentState(
  sessions: Session[] = [],
  openSessionId?: string,
): Record<string, SessionAgentState> {
  const folds = useRef(new Map<string, Fold>());
  const [states, setStates] = useState<Record<string, SessionAgentState>>({});
  // Read inside the subscriber, so changing the open session never tears down
  // and reopens the connection.
  const openRef = useRef(openSessionId);
  openRef.current = openSessionId;
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;

  const publish = useCallback(() => setStates(project(folds.current, sessionsRef.current)), []);

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
    // The cold read: delegates already running when this connection opened
    // never announce themselves again, so one call answers for every
    // session. Subscribed first, so nothing falls in the gap; anything the
    // stream has spoken about since wins over this older snapshot.
    let cancelled = false;
    router
      .listDelegates("running")
      .then((running) => {
        if (cancelled) return;
        for (const d of running) {
          let fold = folds.current.get(d.session_id);
          if (!fold) {
            fold = emptyFold();
            folds.current.set(d.session_id, fold);
          }
          if (fold.heard.has(d.harness_id)) continue;
          fold.delegates.set(d.harness_id, { backgrounded: Boolean(d.backgrounded), startedAt: d.started_at });
        }
        publish();
      })
      // Best effort: without it, live events still fill the count in.
      .catch(() => undefined);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [publish]);

  // Republish when the mirror itself changes — between transitions the stream
  // says nothing, so a refetch is where a cold reader's state arrives. Keyed
  // on only the fields mirroredWorking() reads, so an unrelated list refresh
  // (a rename, a new summary) doesn't re-render every card.
  const mirrorKey = sessions
    .map((s) =>
      [s.id, s.activity, s.agent_state?.state, s.agent_state?.label, s.agent_state?.since].join(
        "\u0001",
      ),
    )
    .join("\u0000");
  useEffect(() => {
    publish();
  }, [mirrorKey, publish]);

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
