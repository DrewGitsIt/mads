import type { ProviderBehavior, ProviderSupport, SessionTrust } from "../../../router/types";

export interface TrustLevel {
  value: SessionTrust;
  /** The plain level name (trustLabel) — pickers mark a warning with ⚠ beside it. */
  label: string;
  /** What the level does, the same on every provider. */
  summary: string;
  /**
   * Set when the selected provider doesn't natively support this level: one
   * line naming the provider and saying what happens instead. Emulated
   * counts as unsupported here — mads answering the harness's permission
   * asks is best-effort, not the harness's guarantee.
   */
  warning?: string;
  /**
   * Set when the selected provider can't run this level, to the level it
   * runs as instead (today only auto_review → limited, on a harness with no
   * reviewer). The warning already says so; this is for callers that need
   * the value.
   */
  fallback?: SessionTrust;
  /**
   * Set when the selected provider can't run this level at all and refuses
   * a session asking for it (fullAccessOnly) — pickers show it disabled.
   */
  unavailable?: boolean;
}

/** Display names, in picker order (pkg/contracts SessionTrusts). */
const TRUST_LABELS: Record<SessionTrust, string> = {
  full: "Full access",
  auto_review: "Auto-review",
  limited: "Limited",
  read_only: "Read-only",
};

/**
 * The plain display name of a trust level — no provider annotation. An
 * unset value is the backend's builtin default, "limited" (see the session
 * dock's TrustSegment); a value this build doesn't know —
 * e.g. one an older or newer mads logged — falls back to the raw string
 * rather than vanishing.
 */
export function trustLabel(value: SessionTrust | string | undefined): string {
  const v = value || "limited";
  return TRUST_LABELS[v as SessionTrust] ?? v;
}

const RESTRICTED: ProviderBehavior[] = ["trust_limited", "trust_read_only", "trust_auto_review"];

/**
 * True when the provider's matrix explicitly declares every restricted level
 * unsupported: it runs at Full access only and refuses a session asking for
 * anything else (hermes — it asks before only the edits and commands its own
 * reviewer flags, so no lower level can be enforced). An omitted row is not
 * a declaration, and no matrix says nothing either way.
 */
export function fullAccessOnly(matrix?: Record<ProviderBehavior, ProviderSupport>): boolean {
  return !!matrix && RESTRICTED.every((b) => matrix[b] === "unsupported");
}

/**
 * The one line a full-access-only provider shows beside its trust, given its
 * display name (providerLabel).
 */
export function fullAccessReason(label: string): string {
  return `${label} can't enforce approvals; it runs at full trust`;
}

/**
 * The level a session asking for `trust` runs at on a provider declaring
 * `matrix` — the UI twin of provider.EffectiveTrust (providers/provider):
 * auto_review falls back to limited when BehaviorTrustAutoReview isn't
 * working. Unlike the warnings below, an unknown provider (no
 * matrix) reports no fallback: falling back is the provider's to declare,
 * and guessing one would put a downgrade on screen that may not happen.
 */
export function trustFallback(
  trust: SessionTrust,
  matrix?: Record<ProviderBehavior, ProviderSupport>,
): SessionTrust | undefined {
  if (trust !== "auto_review" || !matrix || fullAccessOnly(matrix)) return undefined;
  const support = matrix.trust_auto_review ?? "unsupported";
  return support === "unsupported" ? "limited" : undefined;
}

const SUMMARIES: Record<SessionTrust, string> = {
  full: "The agent edits files and runs any command without asking.",
  auto_review: "A reviewer model approves routine actions and asks you only about risky or uncertain ones.",
  limited: "The agent asks you before running commands or other risky actions.",
  read_only: "Blocks edits and commands entirely.",
};

/**
 * The warning line for a restricted level, or undefined when the provider
 * runs it natively. `name` is the provider's display name (providerLabel —
 * passed in, since providerBehaviors imports this module).
 */
function levelWarning(
  value: Exclude<SessionTrust, "full">,
  matrix: Record<ProviderBehavior, ProviderSupport> | undefined,
  name: string,
): string | undefined {
  const head = `${name} does not support this trust level.`;
  // No matrix yet: the provider hasn't said, and a restriction is never
  // claimed before it does.
  if (!matrix) return `${name} hasn't said whether it supports this trust level.`;
  if (fullAccessOnly(matrix)) return `${head} It runs at Full access only.`;
  const behavior: ProviderBehavior = `trust_${value}`;
  switch (matrix[behavior] ?? "unsupported") {
    case "native":
      return undefined;
    case "emulated":
      return `${head} Runs best-effort, with mads answering its permission requests.`;
    case "unsupported":
      return `${head} Runs as ${trustLabel(trustFallback(value, matrix) ?? "full")}.`;
  }
}

/**
 * Builds the trust option list, deriving each restricted level's warning
 * from the selected provider's capability matrix (§11 GET /providers)
 * instead of a hardcoded disclaimer. `providerName` is the provider's
 * display name, e.g. "Goose"; without one the warnings say "This provider".
 */
export function trustLevels(
  matrix?: Record<ProviderBehavior, ProviderSupport>,
  providerName?: string,
): TrustLevel[] {
  const name = providerName ?? "This provider";
  const fullOnly = fullAccessOnly(matrix);
  return (Object.keys(TRUST_LABELS) as SessionTrust[]).map((value) => {
    const level: TrustLevel = { value, label: TRUST_LABELS[value], summary: SUMMARIES[value] };
    if (value === "full") return level;
    level.warning = levelWarning(value, matrix, name);
    level.fallback = trustFallback(value, matrix);
    if (fullOnly) level.unavailable = true;
    return level;
  });
}
