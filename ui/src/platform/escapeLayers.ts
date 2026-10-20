import { useEffect, useRef } from "react";

// Esc closes one layer at a time, topmost first (docs/TASKS-PIPELINE-DESIGN.md):
// a detail sheet, then a stack list or popover, then the lens or focus view.
// Each open layer registers here; the most recently opened one gets the key.
// An open kit Popover keeps its own Escape handling (it listens on the
// document too), so a layer stands aside while one is open, and a handler
// that already claimed the key (preventDefault — e.g. an in-place text edit
// cancelling itself) wins outright.

type Layer = { onEscape: () => void };

const stack: Layer[] = [];
let listening = false;

function handleKeyDown(e: KeyboardEvent) {
  if (e.key !== "Escape" || e.defaultPrevented) return;
  if (document.querySelector("[data-popover-open]")) return;
  const top = stack[stack.length - 1];
  if (!top) return;
  e.preventDefault();
  top.onEscape();
}

function push(layer: Layer) {
  stack.push(layer);
  if (!listening) {
    document.addEventListener("keydown", handleKeyDown);
    listening = true;
  }
}

function remove(layer: Layer) {
  const i = stack.lastIndexOf(layer);
  if (i !== -1) stack.splice(i, 1);
  if (stack.length === 0 && listening) {
    document.removeEventListener("keydown", handleKeyDown);
    listening = false;
  }
}

/** Registers a layer that closes on Escape while `active`. Layers opened later sit on top. */
export function useEscapeLayer(active: boolean, onEscape: () => void) {
  const latest = useRef(onEscape);
  latest.current = onEscape;
  useEffect(() => {
    if (!active) return;
    const layer: Layer = { onEscape: () => latest.current() };
    push(layer);
    return () => remove(layer);
  }, [active]);
}
