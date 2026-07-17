import type { SessionTrust } from "../../../../router/types";

export interface TrustLevel {
  value: SessionTrust;
  label: string;
  /**
   * Declared meaning per contracts.SessionTrust, not an active runtime
   * guarantee — no provider adapter currently gates on trust (see HANDOFF.md).
   * "may" framing avoids overpromising an enforcement flow that isn't wired.
   */
  description: string;
}

export const TRUST_LEVELS: TrustLevel[] = [
  { value: "full", label: "Full access", description: "Agent may edit files and run any command without asking." },
  {
    value: "limited",
    label: "Limited",
    description: "The safer default — agent may edit files and run everyday commands, avoiding destructive ones.",
  },
  {
    value: "read_only",
    label: "Read-only",
    description: "Agent may look around and suggest changes, but may not edit files or run commands.",
  },
];
