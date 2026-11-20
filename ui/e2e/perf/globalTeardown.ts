import { loadPlan } from "./measure";
import { writeReport } from "./report";

/** Writes the run's report from whatever raw results exist, even after a failure. */
export default async function globalTeardown(): Promise<void> {
  const plan = loadPlan();
  if (!plan) return;
  const out = writeReport(plan);
  console.log(`perf report: ${out}`);
}
