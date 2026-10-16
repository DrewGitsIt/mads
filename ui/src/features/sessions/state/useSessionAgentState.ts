import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { DEFAULT_DELEGATE_FADE_MS } from "../rail/DelegateRows";
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
  delegates: Map<string, Delegate>;
  /**
   * Finished delegates still inside the fade window (Settings' "Fade out
   * subagent cards after inactivity") — seen ending live, or loaded by the
   * cold read, so they survive a reload. DelegateRows decides, minute by
   * minute, which are still shown; this only drops what is past the window.
   */
  recent: Map<string, Delegate>;
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
  /** The newest envelope stamp from this session — the stall check's "last heard". */
  heardAt?: string;
}

function emptyFold(): Fold {
  return {
    crashed: false,
    pending: new Map(),
    delegates: new Map(),
    recent: new Map(),
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
 * Finished delegates stay: they are history, and a sleeping session's card
 * keeps showing them for the fade window like any other.
 */
function clearInFlight(fold: Fold): void {
  fold.pending.clear();
  fold.delegates.clear();
}

/** Drops finished delegates past the fade window (Infinity: never). */
function pruneRecent(fold: Fold, fadeMs: number, now: number): void {
  if (fadeMs === Infinity) return;
  for (const [id, d] of fold.recent) {
    const ended = d.ended_at ? Date.parse(d.ended_at) : NaN;
    if (!Number.isFinite(ended) || now - ended >= fadeMs) fold.recent.delete(id);
  }
}

function apply(fold: Fold, env: Envelope, fadeMs: number): void {
  // Parsed, not string-compared: RFC3339Nano drops trailing zeros, so ".1Z" sorts after ".12Z".
  if (env.timestamp && (!fold.heardAt || Date.parse(env.timestamp) > Date.parse(fold.heardAt))) {
    fold.heardAt = env.timestamp;
  }
  switch (env.type) {
    case "session.agent_state": {
      const state = (env.payload ?? {}) as Partial<AgentState>;
      if (state.state !== "working" && state.state !== "idle") return;
      // `since` is the turn's start, held across the turn's label changes so
      // the card's elapsed counter spans the whole turn. Fall back to the
      // envelope's own stamp rather than dropping the transition.
      fold.live = {
        state: state.state,
        label: state.label,
        since: state.since || env.timestamp,
        background: state.background,
      };
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
      // subagent count forever: mirrored() refuses to trust a working
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
      const record = { ...d, started_at: d.started_at ?? env.timestamp } as Delegate;
      if (record.state === "running") {
        fold.delegates.set(record.harness_id, record);
        fold.recent.delete(record.harness_id);
      } else {
        fold.delegates.delete(record.harness_id);
        fold.recent.delete(record.harness_id);
        fold.recent.set(record.harness_id, { ...record, ended_at: record.ended_at ?? env.timestamp });
        pruneRecent(fold, fadeMs, Date.now());
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
 * Only a working state or a background-process count is worth reading, and
 * only on a live session: only the running Runner clears agent_state
 * (pkg/contracts/session.go says so), so a hard-killed process leaves either
 * behind and a card would spin, or wait, forever.
 */
function mirrored(session: Session): AgentState | undefined {
  const state = session.agent_state;
  if (!state || session.activity !== "active") return undefined;
  if (state.state !== "working" && !state.background) return undefined;
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
    if (mirrored(session)) ids.add(session.id);
  }
  for (const id of ids) {
    const fold = folds.get(id);
    const session = byId.get(id);
    // The stream wins wherever it has spoken — including when it says idle.
    // The record only fills the silence before this connection's first
    // transition for that session.
    const agent = fold?.live ?? (session ? mirrored(session) : undefined);
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
      state.subagentsSince = delegates.map((d) => d.started_at).sort()[0];
    }
    const recent = fold ? [...fold.recent.values()] : [];
    if (delegates.length + recent.length > 0) {
      state.delegates = [...delegates, ...recent].sort((a, b) => a.started_at.localeCompare(b.started_at));
    }
    if (fold?.crashed) state.crashed = true;
    if (park) {
      state.awaitingSince = park.at;
      if (park.tool) state.awaitingTool = park.tool;
    }
    if (agent?.state === "working") {
      state.workingSince = agent.since;
      // Heard during this turn — an older stamp is a previous turn's.
      if (fold?.heardAt && Date.parse(fold.heardAt) > Date.parse(agent.since)) state.lastHeardAt = fold.heardAt;
      if (agent.label) state.label = agent.label;
    }
    // Live-only, like the delegates: a sleeping session's processes went
    // with its child, whatever its last count said.
    if (agent?.background && (!session || session.activity === "active")) state.background = agent.background;
    if (fold?.done) state.done = true;
    if (state.crashed || state.awaitingSince || state.workingSince || state.done || state.delegates || state.background) {
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
  /** How long a finished delegate keeps its row (DelegateRows' delegateFadeMs); Infinity: never fades. */
  fadeMs: number = DEFAULT_DELEGATE_FADE_MS,
): Record<string, SessionAgentState> {
  const folds = useRef(new Map<string, Fold>());
  const [states, setStates] = useState<Record<string, SessionAgentState>>({});
  // Read inside the subscriber, so changing the open session never tears down
  // and reopens the connection.
  const openRef = useRef(openSessionId);
  openRef.current = openSessionId;
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;
  const fadeRef = useRef(fadeMs);
  fadeRef.current = fadeMs;

  const publish = useCallback(() => setStates(project(folds.current, sessionsRef.current)), []);

  useEffect(() => {
    const unsubscribe = subscribeToEvents({ types: AGENT_STATE_EVENT_TYPES }, (env) => {
      if (!env.session_id) return;
      let fold = folds.current.get(env.session_id);
      if (!fold) {
        fold = emptyFold();
        folds.current.set(env.session_id, fold);
      }
      apply(fold, env, fadeRef.current);
      // A turn that ends in the session you're looking at was never news.
      if (env.session_id === openRef.current) fold.done = false;
      publish();
    });
    return unsubscribe;
  }, [publish]);

  // The cold read: delegates that started or ended before this connection
  // opened never announce themselves again, so one call answers for every
  // session — the running ones, plus the finished ones the fade window still
  // shows. Declared after the subscription, so nothing falls in the gap;
  // anything the stream has spoken about since wins over this older snapshot.
  // A changed window reads again.
  useEffect(() => {
    let cancelled = false;
    const read =
      fadeMs === 0
        ? router.listDelegates("running")
        : fadeMs === Infinity
          ? router.listDelegates()
          : router.listDelegatesEndedAfter(new Date(Date.now() - fadeMs).toISOString());
    read
      .then((list) => {
        if (cancelled) return;
        for (const d of list) {
          let fold = folds.current.get(d.session_id);
          if (!fold) {
            fold = emptyFold();
            folds.current.set(d.session_id, fold);
          }
          if (fold.heard.has(d.harness_id)) {
            // The stream spoke since this snapshot was taken, and wins —
            // except over a finished record the window merely pruned: the
            // snapshot can't be staler than an ending, and a window that has
            // since widened (Never, once Config loads) wants it back.
            const tracked = fold.delegates.has(d.harness_id) || fold.recent.has(d.harness_id);
            if (tracked || d.state === "running") continue;
          }
          if (d.state === "running") fold.delegates.set(d.harness_id, d);
          else fold.recent.set(d.harness_id, d);
        }
        for (const fold of folds.current.values()) pruneRecent(fold, fadeMs, Date.now());
        publish();
      })
      // Best effort: without it, live events still fill the count in.
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [fadeMs, publish]);

  // Republish when the mirror itself changes — between transitions the stream
  // says nothing, so a refetch is where a cold reader's state arrives. Keyed
  // on only the fields mirrored() reads, so an unrelated list refresh
  // (a rename, a new summary) doesn't re-render every card.
  const mirrorKey = sessions
    .map((s) =>
      [s.id, s.activity, s.agent_state?.state, s.agent_state?.label, s.agent_state?.since, s.agent_state?.background].join(
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
