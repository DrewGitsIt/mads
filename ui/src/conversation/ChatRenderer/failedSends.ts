import type { TranscriptMessage } from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";

/** One earlier send of the same text that failed. */
export interface FailedAttempt {
  error: string;
  timestamp?: string;
}

/** What the conversation shows about a user message's delivery. */
export interface Delivery {
  /** Set when this send failed: the reason, shown in place of a reply. */
  error?: string;
  /** Earlier identical sends that failed, folded into this one — oldest first. */
  attempts: FailedAttempt[];
}

/** A failed send's reason when nothing better is known. */
export const UNDELIVERED = "Your message didn't reach the agent.";

/** Entries that open a new exchange: the human's turn, or one nobody typed. */
const BOUNDARY = new Set<TranscriptMessage["role"]>(["user", "notification", "inbound", "compact"]);

/** The human's own message — not a linked session's or mads's inject. */
const isOwnSend = (m: KeyedRow) => m.role === "user" && !m.attribution;

type Segment = { own: true; user: KeyedRow; replies: KeyedRow[] } | { own: false; rows: KeyedRow[] };

/**
 * Collapses failed sends in the keyed list. A send fails two ways:
 * - the stream said so: its user_message came back `unsaved` (`failed`),
 *   the send never reached the agent. `reason` (the send's HTTP error or the
 *   stream's error frame) explains the latest such send; any other reads
 *   UNDELIVERED.
 * - the transcript says so: its only replies are the harness's error
 *   entries (agent, `is_error`: an expired login, an unreachable API, no
 *   credits). The error entries are dropped and the message carries the
 *   error itself.
 * When the next message is the same text again — a retry, from the Retry
 * button or typed by hand — the failed one is hidden too and folded into
 * that resend as a failed attempt, so one message reads once however many
 * tries it took.
 *
 * A reply that got anywhere — any tool call, any non-error text — makes the
 * exchange delivered, even if it ended in an error. A message with no reply
 * yet (a turn in flight) is not failed.
 *
 * Display only: the provider's own history still holds every attempt.
 * `delivery` is keyed by row key.
 */
export function collapseFailedSends(
  rows: KeyedRow[],
  reason?: string,
): { rows: KeyedRow[]; delivery: Map<string, Delivery> } {
  const segments: Segment[] = [];
  for (const m of rows) {
    const last = segments[segments.length - 1];
    if (isOwnSend(m)) segments.push({ own: true, user: m, replies: [] });
    else if (!last || BOUNDARY.has(m.role)) segments.push({ own: false, rows: [m] });
    else if (last.own) last.replies.push(m);
    else last.rows.push(m);
  }
  const lastFailed = [...rows].reverse().find((r) => r.failed && isOwnSend(r));

  const out: KeyedRow[] = [];
  const delivery = new Map<string, Delivery>();
  let attempts: FailedAttempt[] = [];
  segments.forEach((seg, k) => {
    if (!seg.own) {
      attempts = [];
      out.push(...seg.rows);
      return;
    }
    const failure = seg.user.failed
      ? { error: (seg.user === lastFailed ? reason : undefined) || UNDELIVERED, timestamp: seg.user.timestamp }
      : failureOf(seg.replies);
    // A transcript failure's replies are the harness's errors, carried on
    // the message instead; whatever follows a send the stream failed isn't
    // its reply (it never reached the agent), so it stays.
    const kept = failure && !seg.user.failed ? [] : seg.replies;
    const next = segments[k + 1];
    if (failure && kept.length === 0 && next?.own && sameText(next.user, seg.user)) {
      attempts = [...attempts, failure];
      return;
    }
    if (failure || attempts.length > 0) delivery.set(seg.user.key, { error: failure?.error, attempts });
    attempts = [];
    out.push(seg.user, ...kept);
  });
  return { rows: out, delivery };
}

/** The exchange's failure when every reply is a harness error; else undefined. */
function failureOf(replies: KeyedRow[]): FailedAttempt | undefined {
  if (replies.length === 0 || !replies.every((r) => r.role === "agent" && r.is_error)) return undefined;
  const last = replies[replies.length - 1];
  return { error: last.text?.trim() || "The agent hit an error.", timestamp: last.timestamp };
}

function sameText(a: KeyedRow, b: KeyedRow): boolean {
  return (a.text ?? "").trim() === (b.text ?? "").trim();
}
