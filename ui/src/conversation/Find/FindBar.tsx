import { useCallback, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import { hotkeyLabel } from "../../platform/hotkeys";
import { NO_FIND, type FindRequest, type FindStatus } from "./useFind";
import styles from "./FindBar.module.css";

/** The find bar's state, and what ConversationTab hands ChatRenderer. */
export function useFindBar() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [matchCase, setMatchCase] = useState(false);
  const [skipTools, setSkipTools] = useState(false);
  const [step, setStep] = useState<{ n: number; dir: 1 | -1 }>({ n: 0, dir: 1 });
  const [status, setStatus] = useState<FindStatus>(NO_FIND);
  const inputRef = useRef<HTMLInputElement>(null);
  // Where focus was when the bar opened, to hand it back on close.
  const returnFocus = useRef<HTMLElement | null>(null);

  const request = useMemo<FindRequest | undefined>(
    () => (open ? { query, matchCase, skipTools, step: step.n, dir: step.dir } : undefined),
    [open, query, matchCase, skipTools, step],
  );

  /** Opens the bar (or refocuses it), seeded from `selection` when there is one. */
  const show = useCallback((selection?: string) => {
    if (!inputRef.current || document.activeElement !== inputRef.current) {
      returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    if (selection) setQuery(selection);
    setOpen(true);
    // The input mounts with the bar; focus once it has.
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.select();
    });
  }, []);

  const go = useCallback((dir: 1 | -1) => setStep((s) => ({ n: s.n + 1, dir })), []);

  const close = useCallback(() => {
    setOpen(false);
    setStatus(NO_FIND);
    const back = returnFocus.current;
    returnFocus.current = null;
    if (back?.isConnected) back.focus();
  }, []);

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
      matchCase,
      skipTools,
      status,
      onQuery: setQuery,
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
  const none = query !== "" && status.count === 0;
  const count = query === "" ? "" : none ? "No results" : `${status.current + 1} of ${status.count}`;
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
        {status.ticks.map((t, i) => (
          <i key={i} style={{ top: `${t * 100}%` }} data-current={i === status.current || undefined} />
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
