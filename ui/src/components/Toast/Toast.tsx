import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { Button } from "../Button/Button";
import styles from "./Toast.module.css";

export type ToastTone = "neutral" | "ok" | "warn" | "bad" | "info";

export interface ToastProps {
  tone?: ToastTone;
  title: string;
  description?: string;
  className?: string;
  /**
   * Called when the toast is done: its ✕, Esc while it has focus, its action or
   * target, or its timer running out. Without it the toast has no ✕ and never
   * clears itself — every real toast passes one.
   */
  onDismiss?: () => void;
  /** Undo-style affordance (§7): rendered as a small Button below the description. Dismisses the toast once clicked. */
  action?: { label: string; onClick: () => void; disabled?: boolean; hint?: string };
  /**
   * Where the toast leads — "Open task", the session an error belongs to.
   * Clicking anywhere on the toast's body follows it; toasts without one don't
   * react to clicks, so an unread error can't be waved off by accident.
   */
  target?: { label: string; onClick: () => void };
  /** Milliseconds before it clears itself. Defaults: 15s for warn/bad, 12s otherwise. */
  duration?: number;
}

/** At most this many toasts show at once; later ones wait behind "+N more". */
export const MAX_VISIBLE_TOASTS = 3;

export function defaultToastDuration(tone: ToastTone): number {
  return tone === "bad" || tone === "warn" ? 15_000 : 12_000;
}

interface ViewportApi {
  node: HTMLElement | null;
  /** Mounted toasts, oldest first. */
  ids: string[];
  register: (id: string) => () => void;
}

const ViewportContext = createContext<ViewportApi | null>(null);

/**
 * The one place toasts show: an overlay stack at the top right of whatever
 * contains it (the App shell's surface area), so a toast never pushes a
 * surface's layout. Every <Toast> rendered beneath it portals in here; with no
 * viewport above it (tests, the gallery) a toast renders where it stands.
 */
export function ToastViewport({ children }: { children: ReactNode }) {
  const [node, setNode] = useState<HTMLElement | null>(null);
  const [ids, setIds] = useState<string[]>([]);
  const register = useCallback((id: string) => {
    setIds((prev) => [...prev, id]);
    return () => setIds((prev) => prev.filter((x) => x !== id));
  }, []);
  const api = useMemo(() => ({ node, ids, register }), [node, ids, register]);
  const waiting = ids.length - MAX_VISIBLE_TOASTS;
  return (
    <ViewportContext.Provider value={api}>
      {children}
      <div ref={setNode} className={styles.viewport} aria-live="polite" data-toast-viewport="">
        {waiting > 0 && <div className={styles.more}>+{waiting} more</div>}
      </div>
    </ViewportContext.Provider>
  );
}

/** Whether the window has focus — a toast's timer doesn't run while you're elsewhere. */
function useWindowFocused(): boolean {
  const [focused, setFocused] = useState(() => document.hasFocus());
  useEffect(() => {
    const on = () => setFocused(true);
    const off = () => setFocused(false);
    window.addEventListener("focus", on);
    window.addEventListener("blur", off);
    return () => {
      window.removeEventListener("focus", on);
      window.removeEventListener("blur", off);
    };
  }, []);
  return focused;
}

export function Toast({
  tone = "neutral",
  title,
  description,
  className,
  onDismiss,
  action,
  target,
  duration = defaultToastDuration(tone),
}: ToastProps) {
  const viewport = useContext(ViewportContext);
  const register = viewport?.register;
  const id = useId();
  useLayoutEffect(() => register?.(id), [register, id]);
  const index = viewport ? viewport.ids.indexOf(id) : 0;
  const shown = index >= 0 && index < MAX_VISIBLE_TOASTS;

  // The timer: counts down only while the toast is on screen and nobody is
  // reading it — hovered, focused or the window in the background all pause it.
  const [hovered, setHovered] = useState(false);
  const [focusWithin, setFocusWithin] = useState(false);
  const windowFocused = useWindowFocused();
  const paused = hovered || focusWithin || !windowFocused;
  const timed = shown && !!onDismiss;
  const remaining = useRef(duration);
  const dismissRef = useRef(onDismiss);
  dismissRef.current = onDismiss;
  // A toast whose owner swaps in a new message is a new toast: full time again.
  const content = `${tone}\n${title}\n${description ?? ""}`;
  const timedContent = useRef(content);
  useEffect(() => {
    if (timedContent.current !== content) {
      timedContent.current = content;
      remaining.current = duration;
    }
    if (!timed || paused) return;
    const started = Date.now();
    const t = window.setTimeout(() => dismissRef.current?.(), remaining.current);
    return () => {
      window.clearTimeout(t);
      remaining.current -= Date.now() - started;
    };
  }, [timed, paused, content, duration]);

  if (!shown) return null;

  const followTarget = () => {
    target?.onClick();
    onDismiss?.();
  };
  // The whole body is the target's hit area; its buttons keep their own clicks.
  const onBodyClick = (e: MouseEvent) => {
    if (!target || (e.target as HTMLElement).closest("button")) return;
    followTarget();
  };

  const toast = (
    <div
      role={tone === "bad" ? "alert" : "status"}
      className={[styles.toast, styles[tone], target && styles.hasTarget, className]
        .filter(Boolean)
        .join(" ")}
      style={{ order: index }}
      onClick={onBodyClick}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onFocus={() => setFocusWithin(true)}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setFocusWithin(false);
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape" && onDismiss) {
          e.stopPropagation();
          onDismiss();
        }
      }}
      data-paused={paused || undefined}
    >
      <div className={styles.title}>{title}</div>
      {description && <div className={styles.description}>{description}</div>}
      {(action || target) && (
        <div className={styles.actions}>
          {action && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                action.onClick();
                onDismiss?.();
              }}
              disabled={action.disabled}
            >
              {action.label}
            </Button>
          )}
          {action?.disabled && action.hint && <span className={styles.hint}>{action.hint}</span>}
          {target && (
            <button type="button" className={styles.target} onClick={followTarget}>
              {target.label} <span aria-hidden="true">→</span>
            </button>
          )}
        </div>
      )}
      {onDismiss && (
        <button type="button" className={styles.x} aria-label="Dismiss" title="Dismiss" onClick={onDismiss}>
          ✕
        </button>
      )}
      {timed && (
        <div className={styles.timer} aria-hidden="true">
          <i key={content} style={{ animationDuration: `${duration}ms` }} />
        </div>
      )}
    </div>
  );
  return viewport?.node ? createPortal(toast, viewport.node) : toast;
}

/**
 * For a toast fed by a value someone else owns (a hook's `error`): the value
 * to show, or undefined once dismissed. A new value — or the same one after
 * it cleared — shows again.
 */
export function useDismissible<T>(value: T | null | undefined): [T | undefined, () => void] {
  const [dismissed, setDismissed] = useState<T | null | undefined>(undefined);
  if (value == null && dismissed !== undefined) setDismissed(undefined);
  const shown = value != null && value !== dismissed ? value : undefined;
  return [shown, () => setDismissed(value)];
}
