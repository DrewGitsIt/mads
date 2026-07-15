import type { GenerationPhase } from "../state/useRetroGeneration";

// "3-step generation UI" maps to retro.started + the two retro.progress
// steps (HANDOFF §1) — not three distinct retro.progress values.
export const GENERATION_STEPS = ["Starting", "Gathering", "Generating"] as const;

export type StepStatus = "pending" | "active" | "done";

const PHASE_INDEX: Record<GenerationPhase, number> = {
  idle: -1,
  starting: 0,
  gathering: 1,
  generating: 2,
  completed: 3,
  failed: -1,
  cancelled: -1,
};

export function stepStatus(stepIndex: number, phase: GenerationPhase): StepStatus {
  const current = PHASE_INDEX[phase];
  if (current < 0) return "pending";
  if (stepIndex < current) return "done";
  if (stepIndex === current) return "active";
  return "pending";
}
