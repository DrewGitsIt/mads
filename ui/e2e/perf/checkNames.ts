/**
 * The correctness checks' names (checks.ts), dependency-free so the report
 * can resolve a short name ("a") to the stored one ("a-permission").
 */
export const CHECK_NAMES = [
  "a-permission",
  "b-queue",
  "c-retro-progress",
  "d-pipeline-running",
  "e-provisioning",
  // Real git (perfseed gitfx): git state the UI shows converges and stays fresh.
  "f-git-converge",
  "g-git-updating",
  "h-git-dialogs",
] as const;

/** Checks that watch UI-only state, not anything the bus carries: reported, excluded from bus comparisons. */
export const UI_ONLY_CHECKS = new Set<string>(["c-retro-progress"]);

export function resolveCheck(name: string): string {
  const full = CHECK_NAMES.find((n) => n === name || n.startsWith(`${name}-`));
  if (!full) throw new Error(`unknown check ${name}; have ${CHECK_NAMES.join(", ")}`);
  return full;
}
