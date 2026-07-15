import type { DPA } from "../../../router/types";

// Matches the Router's follow-up-session eligibility rule
// (services/router/internal/api/retros.go: every DPA except a rejected one
// becomes a task) — "accepted" here means "not rejected" (accepted or
// edited), so the CTA count reflects what follow-up creation will actually
// produce.
export function acceptedDpaCount(dpas: DPA[]): number {
  return dpas.filter((dpa) => dpa.status !== "rejected").length;
}
