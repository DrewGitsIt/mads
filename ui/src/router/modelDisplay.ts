import type { Model } from "./types";

// Unambiguous model identity for display: the discovered display name
// (already version-qualified server-side, e.g. "Claude Sonnet 5"), or the
// raw model ID if it isn't in the discovered list (retired/unknown model,
// or a provider whose Models() call failed). Never returns a bare family
// name — every caller that shows a model MUST go through this.
export function modelDisplayName(modelId: string | undefined, models: Model[]): string | undefined {
  if (!modelId) return undefined;
  return models.find((m) => m.id === modelId)?.name ?? modelId;
}

// Context-window size (tokens) for the usage meter. No models-catalog endpoint
// carries this yet (see ContextMeter's data-gap note), so it's a pinned lookup
// keyed by id substring. Every current Claude model is 200k; unknown models
// fall back to that so the meter still reads approximately rather than hiding.
const DEFAULT_CONTEXT_WINDOW = 200_000;

// Per-id overrides go here as models with non-200k windows appear (e.g. a 1M
// beta). Empty today — every current Claude model is 200k.
const CONTEXT_WINDOWS: Record<string, number> = {};

export function contextWindowForModel(modelId: string | undefined): number {
  if (modelId && CONTEXT_WINDOWS[modelId]) return CONTEXT_WINDOWS[modelId];
  return DEFAULT_CONTEXT_WINDOW;
}
