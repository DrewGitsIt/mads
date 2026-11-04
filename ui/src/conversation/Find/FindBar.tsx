import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import { hotkeyLabel } from "../../platform/hotkeys";
import { FIND_CAP, NO_FIND, type FindRequest, type FindStatus } from "./useFind";
import styles from "./FindBar.module.css";

/** Typing searches once it pauses this long: a fast "rubric" is one search, not six. */
export const FIND_DEBOUNCE_MS = 120;

/** The find bar's state, and what ConversationTab hands ChatRenderer. */
export function useFindBar() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // What's searched: the query once typing pauses, or at once when asked
  // (Enter, a seeded selection, an emptied field).
  const [searched, setSearched] = useState("");
  const pending = useRef(0);
  const searchNow = useCallback((q: string) => {
    window.clearTimeout(pending.current);
    pending.current = 0;
    setSearched(q);
  }, []);
  const queryRef = useRef(query);
  queryRef.current = query;
  const onQuery = useCallback(
    (q: string) => {
      setQuery(q);
      queryRef.current = q;
      if (q === "") {
        searchNow("");
        return;
      }
      window.clearTimeout(pending.current);
      pending.current = window.setTimeout(() => {
        pending.current = 0;
        setSearched(q);
      }, FIND_DEBOUNCE_MS);
    },
    [searchNow],
  );
  useEffect(() => () => window.clearTimeout(pending.current), []);
  const [matchCase, setMatchCase] = useState(false);
  const [skipTools, setSkipTools] = useState(false);
  const [step, setStep] = useState<{ n: number; dir: 1 | -1 }>({ n: 0, dir: 1 });
  const [status, setStatus] = useState<FindStatus>(NO_FIND);
  const inputRef = useRef<HTMLInputElement>(null);
  // Where focus was when the bar opened, to hand it back on close.
  const returnFocus = useRef<HTMLElement | null>(null);

  const request = useMemo<FindRequest | undefined>(
    () => (open ? { query: searched, matchCase, skipTools, step: step.n, dir: step.dir } : undefined),
    [open, searched, matchCase, skipTools, step],
  );

  /** Opens the bar (or refocuses it), seeded from `selection` when there is one. */
  const show = useCallback((selection?: string) => {
    if (!inputRef.current || document.activeElement !== inputRef.current) {
      returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    if (selection) {
      setQuery(selection);
      searchNow(selection);
    }
    setOpen(true);
    // The input mounts with the bar; focus once it has.
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.select();
    });
  }, [searchNow]);

  // A step with a search still pending runs the search instead: it lands on
  // the first match, as a fresh query does.
  const go = useCallback(
    (dir: 1 | -1) => {
      if (pending.current) searchNow(queryRef.current);
      else setStep((s) => ({ n: s.n + 1, dir }));
    },
    [searchNow],
  );

  const close = useCallback(() => {
    // Whatever was typed is what reopening shows, and searches.
    searchNow(queryRef.current);
    setOpen(false);
    setStatus(NO_FIND);
    const back = returnFocus.current;
    returnFocus.current = null;
    if (back?.isConnected) back.focus();
  }, [searchNow]);

  return {
    open,
    request,
    show,
    go,
    close,
    onStatus: setStatus,
    bar: {
      inputRef,
      query,
      searched,
      matchCase,
      skipTools,
      status,
      onQuery,
      onMatchCase: () => setMatchCase((v) => !v),
      onSkipTools: () => setSkipTools((v) => !v),
      onStep: go,
      onClose: close,
    },
  };
}

export interface FindBarProps {
  inputRef: RefObject<HTMLInputElement>;
  query: string;
  /** The query the status is for: `query` once typing pauses. */
  searched: string;
  matchCase: boolean;
  skipTools: boolean;
  status: FindStatus;
  onQuery: (query: string) => void;
  onMatchCase: () => void;
  onSkipTools: () => void;
  onStep: (dir: 1 | -1) => void;
  onClose: () => void;
  /** The conversation's scroller: the bar sits over its top-right corner, the ticks down its edge. */
  streamRef: RefObject<HTMLElement | null>;
}

/**
 * Find in conversation (docs/mockups/chat-search.html): a bar over the
 * stream's top-right corner and a tick per match down its right edge. Sits
 * beside the scroller, not in it, so it never scrolls or moves the stream.
 */
export function FindBar({
  inputRef,
  query,
  searched,
  matchCase,
  skipTools,
  status,
  onQuery,
  onMatchCase,
  onSkipTools,
  onStep,
  onClose,
  streamRef,
}: FindBarProps) {
  const box = useStreamBox(streamRef);
  // From the last search, not the keystroke: until typing pauses the bar
  // keeps showing what it last found (nothing, for a first query).
  const none = searched !== "" && status.count === 0;
  // Past the cap the matches in hand are the current one's neighbours, so
  // there's no "n of" to give.
  const count =
    searched === ""
      ? ""
      : none
        ? "No results"
        : status.capped
          ? `More than ${FIND_CAP.toLocaleString()}`
          : `${status.current + 1} of ${status.count}`;
  const next = hotkeyLabel("findNext");
  return (
    <div className={styles.overlay} style={box}>
      <div className={styles.bar} role="search" data-empty={none || undefined}>
        <span className={styles.glass} aria-hidden="true">
          ⌕
        </span>
        <input
          ref={inputRef}
          className={styles.input}
          aria-label="Find in conversation"
          placeholder="Find"
          spellCheck={false}
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              onStep(e.shiftKey ? -1 : 1);
            } else if (e.key === "Escape") {
              e.preventDefault();
              onClose();
            }
          }}
        />
        <span className={styles.count} aria-live="polite" data-none={none || undefined}>
          {count}
        </span>
        <span className={styles.sep} />
        <button
          type="button"
          className={styles.toggle}
          aria-pressed={matchCase}
          aria-label="Match case"
          title="Match case"
          onClick={onMatchCase}
        >
          Aa
        </button>
        <button
          type="button"
          className={`${styles.toggle} ${styles.mono}`}
          aria-pressed={skipTools}
          aria-label="Skip tool calls and output"
          title="Skip tool calls and output"
          onClick={onSkipTools}
        >
          {"{ }"}
        </button>
        <span className={styles.sep} />
        <button
          type="button"
          className={styles.toggle}
          aria-label="Previous match"
          title="Previous (⇧⏎)"
          disabled={status.count === 0}
          onClick={() => onStep(-1)}
        >
          ↑
        </button>
        <button
          type="button"
          className={styles.toggle}
          aria-label="Next match"
          title={`Next (⏎ or ${next})`}
          disabled={status.count === 0}
          onClick={() => onStep(1)}
        >
          ↓
        </button>
        <button type="button" className={styles.toggle} aria-label="Close find" title="Close (Esc)" onClick={onClose}>
          ✕
        </button>
      </div>
      <div className={styles.ticks} aria-hidden="true">
        {status.ticks.map((t) => (
          <i key={t} style={{ top: `${t * 100}%` }} data-current={t === status.currentTick || undefined} />
        ))}
      </div>
    </div>
  );
}

/** The scroller's box within its positioned ancestor, less its scrollbar. */
function useStreamBox(streamRef: RefObject<HTMLElement | null>) {
  const [box, setBox] = useState<{ top: number; left: number; width: number; height: number }>();
  useLayoutEffect(() => {
    const el = streamRef.current;
    if (!el) return;
    const measure = () =>
      setBox({ top: el.offsetTop, left: el.offsetLeft, width: el.clientWidth, height: el.clientHeight });
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [streamRef]);
  return box;
}
