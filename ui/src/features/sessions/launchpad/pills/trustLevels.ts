import type { SessionTrust } from "../../../../router/types";

export interface TrustLevel {
  value: SessionTrust;
  label: string;
  /**
   * No provider adapter gates on trust today (see HANDOFF.md):
   * kirocli.go hardcodes --trust-all-tools on every call and claudecode
   * applies no trust-conditioned flag, so every session currently runs at
   * full access regardless of this setting. Descriptions must say so
   * plainly rather than claim a restriction "limited"/"read_only" don't
   * actually apply.
   */
  description: string;
}

export const TRUST_LEVELS: TrustLevel[] = [
  {
    value: "full",
    label: "Full access",
    description: "Agent edits files and runs any command without asking — how every session behaves today.",
  },
  {
    value: "limited",
    label: "Limited",
    description: "Meant to restrict the agent to everyday changes, but not enforced yet — runs as Full access today.",
  },
  {
    value: "read_only",
    label: "Read-only",
    description: "Meant to block edits and commands entirely, but not enforced yet — runs as Full access today.",
  },
];
