import { useEffect, useState } from "react";
import type { Task, TranscriptMessage } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";
import type { PipelineOutputSource, PipelineTurnsSource } from "../client/pipelineClient";
import { AgentOutputView } from "./AgentOutputView";
import { AgentFeedView } from "./AgentFeedView";

export interface DrillInFeedProps {
  task: Task;
  attempts: StageAttempt[];
  outputSource: PipelineOutputSource;
  turnsSource: PipelineTurnsSource;
  /** Show the raw log even when turns arrive (the lens's Raw log toggle). */
  raw?: boolean;
  /** Passed to AgentFeedView. */
  showHeader?: boolean;
}

/**
 * Chooses between the transparency-first turns feed and the raw prose log,
 * per task. A provider that announces a session ref (e.g. claude-code)
 * eventually delivers a non-empty turns frame; a provider that doesn't
 * (today's fake, or an old run predating this feature) never does, so this
 * falls back to the tee'd output exactly as it rendered before the feed
 * existed — the fallback layering DESIGN point 3 requires.
 */
export function DrillInFeed({
  task,
  attempts,
  outputSource,
  turnsSource,
  raw = false,
  showHeader,
}: DrillInFeedProps) {
  const [turns, setTurns] = useState<TranscriptMessage[]>([]);
  useEffect(() => turnsSource.onTurns(setTurns), [turnsSource]);

  if (!raw && turns.length > 0) {
    return <AgentFeedView task={task} attempts={attempts} turns={turns} showHeader={showHeader} />;
  }
  return <AgentOutputView source={outputSource} />;
}
