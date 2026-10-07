import type { Model, Usage } from "./types";

// Unambiguous model identity for display: the discovered display name
// (already version-qualified server-side, e.g. "Claude Sonnet 5"), or the
// raw model ID if it isn't in the discovered list (retired/unknown model,
// or a provider whose Models() call failed). Never returns a bare family
// name — every caller that shows a model MUST go through this.
export function modelDisplayName(modelId: string | undefined, models: Model[]): string | undefined {
  if (!modelId) return undefined;
  return models.find((m) => m.id === modelId)?.name ?? modelId;
}

// Context-window size (tokens) for the dock's context rim, when the provider
// didn't report one (usage.context_window wins). No models-catalog endpoint
// carries this yet, so it's a pinned lookup keyed by id substring. Every
// current Claude model is 200k; unknown models fall back to that so the rim
// still reads approximately rather than hiding.
const DEFAULT_CONTEXT_WINDOW = 200_000;

// Per-id overrides go here as models with non-200k windows appear (e.g. a 1M
// beta). Empty today — every current Claude model is 200k.
const CONTEXT_WINDOWS: Record<string, number> = {};

export function contextWindowForModel(modelId: string | undefined): number {
  if (modelId && CONTEXT_WINDOWS[modelId]) return CONTEXT_WINDOWS[modelId];
  return DEFAULT_CONTEXT_WINDOW;
}

export interface ContextOccupancy {
  /** Prompt-side tokens the next request will carry. */
  used: number;
  window: number;
  /** used/window as a 0-100 integer, clamped. */
  pct: number;
}

/**
 * Context occupancy for a turn's usage record. Prefers usage.context_tokens —
 * the prompt side of the turn's FINAL API call, which is what the next request
 * actually carries. The summed token fields are only a fallback for providers
 * that don't report per-call occupancy: on claude-code they are a per-turn
 * billing aggregate (cache reads summed across every tool round-trip) that
 * overstates occupancy by roughly the iteration count.
 *
 * Shared so the places that show occupancy — the session dock's context rim
 * (and its + menu's context row) and the session rail's tooltip — cannot
 * disagree. Undefined when there is no usage or no window to judge it against.
 */
export function contextOccupancy(
  usage: Usage | undefined,
  fallbackWindow: number | undefined,
): ContextOccupancy | undefined {
  const window = usage?.context_window || fallbackWindow;
  if (usage === undefined || window === undefined || window <= 0) return undefined;
  const used =
    usage.context_tokens ||
    usage.input_tokens + (usage.cache_read_input_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0);
  return { used, window, pct: Math.min(100, Math.round((used / window) * 100)) };
}
