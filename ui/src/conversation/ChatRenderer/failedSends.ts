import type { TranscriptMessage } from "../../router/types";

/** One earlier send of the same text that the harness failed. */
export interface FailedAttempt {
  error: string;
  timestamp?: string;
}

/** What the conversation shows about a user message's delivery. */
export interface Delivery {
  /** Set when this send failed: the harness's error, shown in place of a reply. */
  error?: string;
  /** Earlier identical sends that failed, folded into this one — oldest first. */
  attempts: FailedAttempt[];
}

/** Entries that open a new exchange: the human's turn, or one nobody typed. */
const BOUNDARY = new Set<TranscriptMessage["role"]>(["user", "notification", "inbound", "compact"]);

/** The human's own message — not a linked session's or mads's inject. */
const isOwnSend = (m: TranscriptMessage) => m.role === "user" && !m.attribution;

type Segment =
  | { own: true; user: TranscriptMessage; replies: TranscriptMessage[] }
  | { own: false; messages: TranscriptMessage[] };

/**
 * Collapses failed sends in a transcript. A user message whose only replies
 * are the harness's error entries (agent, `is_error`: an expired login, an
 * unreachable API, no credits) never reached the agent: the error entries are
 * dropped and the message carries the error itself (`delivery.error`). When
 * the next message is the same text again — a retry, from the Retry button
 * or typed by hand — the failed one is hidden too and folded into that
 * resend as a failed attempt, so one message reads once however many tries
 * it took.
 *
 * A reply that got anywhere — any tool call, any non-error text — makes the
 * exchange delivered, even if it ended in an error. A message with no reply
 * yet (a turn in flight) is not failed.
 *
 * Display only: the provider's own history still holds every attempt.
 * Message objects pass through by reference, so `delivery` can key them.
 */
export function collapseFailedSends(messages: TranscriptMessage[]): {
  messages: TranscriptMessage[];
  delivery: Map<TranscriptMessage, Delivery>;
} {
  const segments: Segment[] = [];
  for (const m of messages) {
    const last = segments[segments.length - 1];
    if (isOwnSend(m)) segments.push({ own: true, user: m, replies: [] });
    else if (!last || BOUNDARY.has(m.role)) segments.push({ own: false, messages: [m] });
    else if (last.own) last.replies.push(m);
    else last.messages.push(m);
  }

  const out: TranscriptMessage[] = [];
  const delivery = new Map<TranscriptMessage, Delivery>();
  let attempts: FailedAttempt[] = [];
  segments.forEach((seg, k) => {
    if (!seg.own) {
      attempts = [];
      out.push(...seg.messages);
      return;
    }
    const failure = failureOf(seg.replies);
    const next = segments[k + 1];
    if (failure && next?.own && sameText(next.user, seg.user)) {
      attempts = [...attempts, failure];
      return;
    }
    if (failure || attempts.length > 0) delivery.set(seg.user, { error: failure?.error, attempts });
    attempts = [];
    out.push(seg.user, ...(failure ? [] : seg.replies));
  });
  return { messages: out, delivery };
}

/** The exchange's failure when every reply is a harness error; else undefined. */
function failureOf(replies: TranscriptMessage[]): FailedAttempt | undefined {
  if (replies.length === 0 || !replies.every((r) => r.role === "agent" && r.is_error)) return undefined;
  const last = replies[replies.length - 1];
  return { error: last.text?.trim() || "The agent hit an error.", timestamp: last.timestamp };
}

function sameText(a: TranscriptMessage, b: TranscriptMessage): boolean {
  return (a.text ?? "").trim() === (b.text ?? "").trim();
}
