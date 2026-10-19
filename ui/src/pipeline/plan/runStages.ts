import { parse } from "../../library/templates/templateSync";
import type { Template } from "../../router/types";

/** One stage of the template a run uses, as the metro, lens and graph draw it. */
export interface RunStage {
  name: string;
  kind: "agent" | "command";
  /** on_fail.loop_back: the stage a failure here sends the task back to. */
  loopBack?: string;
  maxLoops?: number;
  /** on_fail.then — "block" ends the task blocked once loops run out. */
  then?: string;
  /** The stage's `when:` condition, if any (it can skip the stage per task). */
  when?: string;
}

/**
 * The stages to draw for a run: the template's own stage list, in order,
 * plus any stage the run's events name that the template no longer has (a
 * run keeps a frozen copy, so the saved template may have changed since).
 * With no template — or one that won't parse — the run's own first-seen
 * stage order (the fold's `columns`) is all there is.
 */
export function runStages(template: Template | undefined, columns: string[] = []): RunStage[] {
  const parsed = template ? parse(template.source).parsed : null;
  const stages: RunStage[] = (parsed?.stages ?? []).map((s) => ({
    name: s.name,
    kind: s.command ? "command" : "agent",
    loopBack: s.on_fail?.loop_back,
    maxLoops: s.on_fail?.max_loops,
    then: s.on_fail?.then,
    when: s.when,
  }));
  const known = new Set(stages.map((s) => s.name));
  for (const name of columns) {
    if (!known.has(name)) {
      stages.push({ name, kind: "agent" });
      known.add(name);
    }
  }
  return stages;
}
