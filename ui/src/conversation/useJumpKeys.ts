import { useEffect, useRef, type RefObject } from "react";
import { prefersReducedMotion } from "../tasks/GraphView/usePrefersReducedMotion";

/** Two presses of the same jump at most this far apart are a double tap. */
export const DOUBLE_TAP_MS = 400;

export type JumpDirection = "up" | "down";

export interface JumpKeys {
  /**
   * One Cmd/Ctrl+↑/↓: hop to the previous/next of something (the user's own
   * messages). Returns false when there's nothing to hop to, so the key is
   * left to whoever else wants it (a caret in the composer).
   */
  hop?: (dir: JumpDirection, el: HTMLElement) => boolean;
  /** A double tap of Cmd/Ctrl+↑: the beginning of the conversation. */
  toTop: (el: HTMLElement) => void;
  /** A double tap of Cmd/Ctrl+↓: the end of it, following again. */
  toBottom: (el: HTMLElement) => void;
}

/** A smooth scroll, or an instant one under reduce motion. */
export function glideTo(el: HTMLElement, top: number) {
  el.scrollTo({ top, behavior: prefersReducedMotion() ? "auto" : "smooth" });
}

/**
 * Cmd/Ctrl+↑/↓ in a conversation scroller: one tap hops (`hop`), a second
 * tap of the same arrow within DOUBLE_TAP_MS goes all the way to that end.
 * A held-down key only hops. Ignored while the scroller is hidden (another
 * workspace tab is showing).
 */
export function useJumpKeys(scroller: RefObject<HTMLElement | null>, keys: JumpKeys) {
  const latest = useRef(keys);
  latest.current = keys;
  useEffect(() => {
    let last: { dir: JumpDirection; at: number } | null = null;
    function onKey(e: KeyboardEvent) {
      if (!(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) return;
      if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
      const el = scroller.current;
      if (!el || (el.checkVisibility && !el.checkVisibility())) return;
      const dir: JumpDirection = e.key === "ArrowUp" ? "up" : "down";
      const now = performance.now();
      const double = !e.repeat && last?.dir === dir && now - last.at <= DOUBLE_TAP_MS;
      if (double) {
        last = null;
        e.preventDefault();
        if (dir === "up") latest.current.toTop(el);
        else latest.current.toBottom(el);
        return;
      }
      last = e.repeat ? null : { dir, at: now };
      if (latest.current.hop?.(dir, el)) e.preventDefault();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [scroller]);
}
