import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button } from "../../components/Button/Button";
import type { HeldUpdate } from "../../router/types";
import { Markdown } from "../ChatRenderer/markdown";
import { formatMessageTime, parseMessageTime } from "../ChatRenderer/messageTime";
import { HELD_REVEAL_EVENT, takeHeldReveal, type HeldRevealDetail } from "./heldRevealSignal";
import { displayText, sourceNames, summaryLine, updatesNoun } from "./heldText";
import styles from "./HeldUpdates.module.css";

export interface HeldUpdatesProps {
  sessionId: string;
  held: HeldUpdate[];
  discarded: HeldUpdate[];
  onDiscard: (ids: number[]) => void;
  onRestore: (ids: number[]) => void;
  /** "Send updates only". */
  onDeliver: () => void;
  /** A delivery is in flight: Send updates only and Discard all wait. */
  delivering?: boolean;
  /** Brings the list into view — the rail's "N updates waiting" asked (heldRevealSignal). */
  onReveal?: (el: HTMLElement) => void;
}

/** The updates from one session, newest first. */
type Group = { id: string; name: string; updates: HeldUpdate[] };

/** Groups by source session; newest update first in each, and the group with the newest update first (mockup C). */
function groupBySource(updates: HeldUpdate[]): Group[] {
  const groups = new Map<string, Group>();
  for (const u of [...updates].reverse()) {
    const id = u.source_session_id || u.source_name;
    const g = groups.get(id) ?? { id, name: u.source_name || "a linked session", updates: [] };
    g.updates.push(u);
    groups.set(id, g);
  }
  return [...groups.values()];
}

/**
 * The held link updates at the end of the conversation
 * (docs/HELD-UPDATES-DESIGN.md, mockup C): pushes that arrived while the
 * session slept, which go to the agent with the user's next message. One
 * compact row per update, grouped by the session it came from; a row opens
 * to the full text. × on a row discards it; the footer discards all, or
 * sends them on their own as one turn. What was discarded since the last
 * send stays as one "N discarded · Undo" line until the next send drains
 * it — there is no record of it in the timeline after that (decision 9).
 *
 * Each row's text is ⌘F-findable (`data-find`), the time and buttons are not.
 */
export function HeldUpdates({
  sessionId,
  held,
  discarded,
  onDiscard,
  onRestore,
  onDeliver,
  delivering = false,
  onReveal,
}: HeldUpdatesProps) {
  const rootRef = useRef<HTMLElement>(null);
  const shown = held.length + discarded.length > 0;

  // The rail's ask, parked before this mounted, or fired while it shows.
  const revealRef = useRef(onReveal);
  revealRef.current = onReveal;
  useLayoutEffect(() => {
    if (shown && rootRef.current && takeHeldReveal(sessionId)) revealRef.current?.(rootRef.current);
  }, [shown, sessionId]);
  useEffect(() => {
    const on = (e: Event) => {
      if ((e as CustomEvent<HeldRevealDetail>).detail?.sessionId !== sessionId) return;
      if (rootRef.current && takeHeldReveal(sessionId)) revealRef.current?.(rootRef.current);
    };
    window.addEventListener(HELD_REVEAL_EVENT, on);
    return () => window.removeEventListener(HELD_REVEAL_EVENT, on);
  }, [sessionId]);

  if (!shown) return null;
  const groups = groupBySource(held);
  const discardedLine = discarded.length > 0 && (
    <div className={styles.discarded}>
      <span>
        {discarded.length} discarded · {sourceNames(discarded).join(", ")}
      </span>
      <button type="button" className={styles.undo} onClick={() => onRestore(discarded.map((u) => u.id))}>
        Undo
      </button>
    </div>
  );

  return (
    <section ref={rootRef} className={styles.root} aria-label="Held updates" data-held-updates="">
      {held.length > 0 ? (
        <div className={styles.block}>
          <div className={styles.head}>
            <span className={styles.title}>{updatesNoun(held.length)} held</span>
            <span className={styles.sub}>
              {groups.length > 1 && `from ${groups.length} sessions · `}go with your next message
            </span>
          </div>
          {groups.map((g) => (
            <div key={g.id} className={styles.group}>
              <div className={styles.groupName}>{g.name}</div>
              {g.updates.map((u) => (
                <HeldRow key={u.id} update={u} onDiscard={() => onDiscard([u.id])} />
              ))}
            </div>
          ))}
          {discardedLine}
          <div className={styles.foot}>
            <Button
              variant="ghost"
              size="sm"
              disabled={delivering}
              onClick={() => onDiscard(held.map((u) => u.id))}
            >
              Discard all
            </Button>
            <span className={styles.spacer} />
            <Button variant="secondary" size="sm" disabled={delivering} onClick={onDeliver}>
              {delivering ? "Sending…" : "Send updates only"}
            </Button>
          </div>
        </div>
      ) : (
        // Everything discarded: the session stays asleep, and only the way
        // back is left until the next send.
        discardedLine
      )}
    </section>
  );
}

/** One held update: a line of its text, the full text on click. */
function HeldRow({ update, onDiscard }: { update: HeldUpdate; onDiscard: () => void }) {
  const [open, setOpen] = useState(false);
  const at = parseMessageTime(update.created_at);
  const findKey = `held:${update.id}`;
  return (
    <div className={styles.row} data-open={open || undefined}>
      <button
        type="button"
        className={styles.toggle}
        aria-expanded={open}
        aria-label={open ? "Collapse update" : "Expand update"}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={styles.caret} aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      <div className={styles.body} data-find={findKey} onClick={() => !open && setOpen(true)}>
        {open ? (
          <Markdown className={styles.prose} text={displayText(update.text)} breaks />
        ) : (
          <p className={styles.line}>{summaryLine(update.text)}</p>
        )}
      </div>
      {at && (
        <time className={styles.time} dateTime={at.toISOString()} title={at.toLocaleString()}>
          {formatMessageTime(at)}
        </time>
      )}
      <button
        type="button"
        className={styles.x}
        title="Discard"
        aria-label={`Discard update from ${update.source_name || "a linked session"}`}
        onClick={onDiscard}
      >
        ×
      </button>
    </div>
  );
}
