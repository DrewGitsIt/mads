import { useEffect, useRef, type RefObject } from "react";

/** Two Enters at most this far apart are a double press. */
export const DOUBLE_ENTER_MS = 400;

/**
 * Whether an Enter on `target` is a free one: the session's composer while
 * it's empty (an Enter there sends nothing), or anywhere in the tab that
 * isn't a field or a control with an Enter of its own. An Enter that sends
 * typed text isn't — the composer still holds it when the keydown reaches
 * the window.
 */
export function isFreeEnter(target: EventTarget | null, scope: HTMLElement | null): boolean {
  if (!scope || !(target instanceof HTMLElement)) return false;
  if (target === target.ownerDocument.body) return true;
  if (!scope.contains(target)) return false;
  if (target instanceof HTMLTextAreaElement)
    return !!target.closest('[data-testid="session-dock"]') && target.value.trim() === "";
  if (target.isContentEditable) return false;
  return !target.closest(CONTROLS);
}

// What has an Enter of its own. A focusable scroller (the stream) isn't one.
const CONTROLS =
  "input, select, button, a[href], summary, [role=button], [role=textbox], [role=menuitem], [role=option], [role=tab], [role=checkbox], [role=switch]";

/**
 * Calls `onDouble` on two free Enters (isFreeEnter) in quick succession,
 * while `enabled` — the paced reveal is behind, so the second press shows
 * all of it. Anything else in between (another key, an Enter that sent or
 * belonged to a control) starts the count over. Ignored while the tab is
 * hidden behind another workspace tab.
 */
export function useDoubleEnter(scopeRef: RefObject<HTMLElement | null>, enabled: boolean, onDouble: () => void) {
  const latest = useRef({ enabled, onDouble });
  latest.current = { enabled, onDouble };
  useEffect(() => {
    let last = -Infinity;
    function onKey(e: KeyboardEvent) {
      const scope = scopeRef.current;
      const plain = e.key === "Enter" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey && !e.isComposing;
      if (!plain || e.repeat || !scope || (scope.checkVisibility && !scope.checkVisibility()) || !isFreeEnter(e.target, scope)) {
        last = -Infinity;
        return;
      }
      const now = performance.now();
      if (now - last <= DOUBLE_ENTER_MS && latest.current.enabled) {
        last = -Infinity;
        latest.current.onDouble();
      } else last = now;
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [scopeRef]);
}
