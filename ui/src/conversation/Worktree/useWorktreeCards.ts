import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import { WORKTREE_CARD_EVENT_TYPES } from "../../router/sessionEventTypes";
import { getAllEvents } from "../../router/eventPages";
import type { CommitResult, Envelope, LandResult, UndoCommitResult } from "../../router/types";
import {
  foldWorktreeEvents,
  landCardFromResult,
  withEchoes,
  type WorktreeCardModel,
  type WorktreeEchoes,
} from "./worktreeCards";

export interface WorktreeCards {
  cards: WorktreeCardModel[];
  /** A Land is in flight — the dock shows "Landing…" and won't start another. */
  landing: boolean;
  /** A one-click Commit is in flight. */
  committing: boolean;
  /** POST /land. Resolves the outcome (each is a card); rejects only on an unexpected failure. */
  land: () => Promise<LandResult | undefined>;
  /** The one-click Commit: every session file, message drafted server-side. Rejects with CommitRefusedError. */
  commitAll: () => Promise<CommitResult>;
  /** Shows a Commit dialog's result until its commit.created arrives. */
  noteCommit: (result: CommitResult) => void;
  undo: (hash: string) => Promise<UndoCommitResult>;
}

function subjectOf(message: string): string {
  return message.split("\n", 1)[0] ?? "";
}

/**
 * The conversation's Commit/Land cards and the actions that make them, for
 * one session. Cards come from the event log (replay at mount, then the
 * shared live bus); an action's own answer is held as an echo only until
 * its event arrives, so each outcome shows once whichever lands first.
 */
export function useWorktreeCards(client: RouterClient, sessionId: string): WorktreeCards {
  const [envelopes, setEnvelopes] = useState<Envelope[]>([]);
  const [echoes, setEchoes] = useState<WorktreeEchoes>({ commits: [] });
  const [undone, setUndone] = useState<ReadonlySet<string>>(new Set());
  const [landing, setLanding] = useState(false);
  const [committing, setCommitting] = useState(false);
  // The newest worktree event seen — a Land echo stands only until a Land
  // outcome newer than its click is logged.
  const maxSeq = useRef(0);
  const landingRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    setEnvelopes([]);
    setEchoes({ commits: [] });
    setUndone(new Set());
    maxSeq.current = 0;
    const add = (incoming: Envelope[]) => {
      for (const e of incoming) maxSeq.current = Math.max(maxSeq.current, e.seq);
      // Replay and live overlap at the seam; the fold drops repeated seqs.
      setEnvelopes((cur) => [...cur, ...incoming]);
    };
    void getAllEvents(client, { sessionId, types: WORKTREE_CARD_EVENT_TYPES })
      .then((envs) => {
        if (!cancelled) add(envs);
      })
      .catch(() => {});
    const unsubscribe = subscribeToEvents({ sessionId, types: WORKTREE_CARD_EVENT_TYPES }, (env) =>
      add([env]),
    );
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [client, sessionId]);

  const folded = useMemo(() => foldWorktreeEvents(envelopes), [envelopes]);
  const cards = useMemo(() => {
    const all = withEchoes(folded, echoes);
    if (undone.size === 0) return all;
    return all.map((c) => (c.kind === "committed" && undone.has(c.hash) ? { ...c, undone: true, tip: false } : c));
  }, [folded, echoes, undone]);

  const land = useCallback(async () => {
    if (landingRef.current) return undefined;
    landingRef.current = true;
    setLanding(true);
    const afterSeq = maxSeq.current;
    try {
      const result = await client.landSession(sessionId);
      setEchoes((e) => ({ ...e, land: { afterSeq, card: landCardFromResult(result, new Date().toISOString()) } }));
      return result;
    } finally {
      landingRef.current = false;
      setLanding(false);
    }
  }, [client, sessionId]);

  const noteCommit = useCallback((result: CommitResult) => {
    const files = result.paths.filter((p) => p.status === "committed").length;
    setEchoes((e) => ({
      ...e,
      commits: [
        ...e.commits,
        {
          kind: "committed",
          id: `commit-echo-${result.hash}`,
          seq: Number.POSITIVE_INFINITY,
          timestamp: new Date().toISOString(),
          hash: result.hash,
          subject: subjectOf(result.message),
          files: files || undefined,
          drafted: result.drafted === true,
          undone: false,
          tip: true,
        },
      ],
    }));
  }, []);

  const commitAll = useCallback(async () => {
    setCommitting(true);
    try {
      const result = await client.commitSession(sessionId, {});
      noteCommit(result);
      return result;
    } finally {
      setCommitting(false);
    }
  }, [client, sessionId, noteCommit]);

  const undo = useCallback(
    async (hash: string) => {
      const result = await client.undoCommit(sessionId, hash);
      if (result.undone) setUndone((s) => new Set(s).add(hash));
      return result;
    },
    [client, sessionId],
  );

  return { cards, landing, committing, land, commitAll, noteCommit, undo };
}
