import type { ProviderBehavior, ProviderSupport, SessionTrust } from "../../../../router/types";

export interface TrustLevel {
  value: SessionTrust;
  label: string;
  description: string;
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

/**
 * Builds the trust option list, deriving each restricted level's honesty
 * from the selected provider's capability matrix (§11 GET /providers)
 * instead of a hardcoded disclaimer.
 */
export function trustLevels(
  matrix?: Record<ProviderBehavior, ProviderSupport>,
): TrustLevel[] {
  return [
    {
      value: "full",
      label: "Full access",
      description: "Agent edits files and runs any command without asking.",
    },
    {
      value: "limited",
      label: "Limited",
      description: `Agent asks you before running commands or other risky actions — ${enforcementNote(trustSupport(matrix, "trust_limited"))}`,
    },
    {
      value: "read_only",
      label: "Read-only",
      description: `Blocks edits and commands entirely — ${enforcementNote(trustSupport(matrix, "trust_read_only"))}`,
    },
  ];
}
