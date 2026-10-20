import type { ProviderBehavior, ProviderSupport, SessionTrust } from "../../../router/types";

export interface TrustLevel {
  value: SessionTrust;
  label: string;
  description: string;
  /**
   * Set when the selected provider can't run this level, to the level it
   * runs as instead (today only auto_review → limited, on a harness with no
   * reviewer). The label already says so; this is for callers that need
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

/**
 * BehaviorTrustLimited/BehaviorTrustReadOnly (providers/provider/matrix.go)
 * are how each provider adapter declares whether it actually enforces that
 * trust level. No matrix (provider not yet selected) is treated the same as
 * "unsupported": never claim a restriction applies before the selected
 * provider has said so.
 */
function trustSupport(
  matrix: Record<ProviderBehavior, ProviderSupport> | undefined,
  behavior: ProviderBehavior,
): ProviderSupport {
  return matrix?.[behavior] ?? "unsupported";
}

function enforcementNote(support: ProviderSupport): string {
  switch (support) {
    case "native":
      return "enforced by the selected provider.";
    case "emulated":
      return "best-effort with the selected provider — not a hard guarantee.";
    case "unsupported":
      return "not enforced by the selected provider — runs as Full access.";
  }
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
 * The level a session asking for `trust` runs at on a provider declaring
 * `matrix` — the UI twin of provider.EffectiveTrust (providers/provider):
 * auto_review falls back to limited when BehaviorTrustAutoReview isn't
 * working. Unlike the restriction notes above, an unknown provider (no
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

/**
 * Auto-review's honesty line. Worded apart from enforcementNote on purpose:
 * a missing reviewer doesn't loosen anything — the session asks more, as
 * Limited — so "not enforced … runs as Full access" would be wrong here.
 */
function reviewerNote(
  matrix: Record<ProviderBehavior, ProviderSupport> | undefined,
  provider: string | undefined,
): string {
  if (!matrix) return "runs as Limited on a provider with no reviewer.";
  switch (matrix.trust_auto_review ?? "unsupported") {
    case "native":
      return "the selected provider's harness runs the reviewer.";
    case "emulated":
      return "the selected provider emulates the reviewer — best-effort.";
    case "unsupported":
      return `${provider ?? "the selected provider"} has no reviewer, so this runs as Limited.`;
  }
}

/**
 * Builds the trust option list, deriving each restricted level's honesty
 * from the selected provider's capability matrix (§11 GET /providers)
 * instead of a hardcoded disclaimer. `provider` names the provider in
 * Auto-review's fallback annotation — "(runs as Limited on kiro-cli)" —
 * when its matrix says it has no reviewer.
 */
export function trustLevels(
  matrix?: Record<ProviderBehavior, ProviderSupport>,
  provider?: string,
): TrustLevel[] {
  const autoFallback = trustFallback("auto_review", matrix);
  const where = provider ?? "this provider";
  if (fullAccessOnly(matrix)) {
    const note = `not available on ${where}, which runs at Full access only.`;
    return [
      { value: "full", label: TRUST_LABELS.full, description: "Agent edits files and runs any command without asking." },
      { value: "auto_review", label: TRUST_LABELS.auto_review, description: `A reviewer model approves routine actions — ${note}`, unavailable: true },
      { value: "limited", label: TRUST_LABELS.limited, description: `Agent asks you before risky actions — ${note}`, unavailable: true },
      { value: "read_only", label: TRUST_LABELS.read_only, description: `Blocks edits and commands entirely — ${note}`, unavailable: true },
    ];
  }
  return [
    {
      value: "full",
      label: TRUST_LABELS.full,
      description: "Agent edits files and runs any command without asking.",
    },
    {
      value: "auto_review",
      label: autoFallback
        ? `${TRUST_LABELS.auto_review} (runs as ${trustLabel(autoFallback)} on ${where})`
        : TRUST_LABELS.auto_review,
      description: `A reviewer model approves routine actions and asks you only about risky or uncertain ones — ${reviewerNote(matrix, provider)}`,
      fallback: autoFallback,
    },
    {
      value: "limited",
      label: TRUST_LABELS.limited,
      description: `Agent asks you before running commands or other risky actions — ${enforcementNote(trustSupport(matrix, "trust_limited"))}`,
    },
    {
      value: "read_only",
      label: TRUST_LABELS.read_only,
      description: `Blocks edits and commands entirely — ${enforcementNote(trustSupport(matrix, "trust_read_only"))}`,
    },
  ];
}
