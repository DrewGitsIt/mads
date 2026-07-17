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
