/**
 * The fake provider's script for pipeline runs the Router launches (the
 * Tasks tab's Run button → POST /pipelines → services/pipeline
 * -fake-scenario). One file serves the whole suite, so every rule keys on a
 * task title only one spec uses; prompts no rule matches get the fake's
 * instant "ok" (a pass).
 *
 * An agent stage's prompt is "Pipeline stage: <stage>\nTask: <title>\n",
 * then (for a task with no description or criteria) either the closing
 * "When you are done…" line or, once a downstream stage has failed, "A
 * previous attempt failed downstream…" (engine.go's taskPrompt). Matching
 * on what follows the title tells a stage's first attempt from a loop-back.
 */

/** tasks-pipeline.spec: the task the journey zooms into. Review fails it once, so executor runs twice. */
export const JOURNEY_LENS_TASK = "tp journey: lens task";
/** tasks-pipeline.spec: left out of the first run, then the slow task the second run is cancelled under. */
export const JOURNEY_ROOT_TASK = "tp journey: left-out root";

export const ATTEMPT_ONE_LINES = Array.from({ length: 8 }, (_, i) => `attempt-one step ${i + 1}`);
export const ATTEMPT_TWO_LINES = Array.from({ length: 8 }, (_, i) => `attempt-two step ${i + 1}`);
export const REVIEW_FAILURE = "add a test for the reuse path";

const quote = (s: string) => JSON.stringify(s);

function chunks(lines: string[], delayMs: number): string[] {
  return [
    "    chunks:",
    ...lines.map((l) => `      - ${quote(`${l}\n`)}`),
    `      - ${quote("STAGE: pass")}`,
    `    delay_ms: ${delayMs}`,
  ];
}

const firstAttempt = (stage: string, title: string) => `Pipeline stage: ${stage}\nTask: ${title}\n\nWhen you are done`;
const loopBack = (stage: string, title: string) => `Pipeline stage: ${stage}\nTask: ${title}\n\nA previous attempt failed downstream`;

export const PIPELINE_SCENARIO = [
  "name: e2e-router-pipelines",
  "rules:",
  // Review fails the first time only: its second attempt's prompt carries
  // the feedback, so this rule no longer matches and it passes.
  `  - match: ${quote(firstAttempt("code_review", JOURNEY_LENS_TASK))}`,
  `    reply: ${quote(`Missing coverage.\nSTAGE: fail — ${REVIEW_FAILURE}`)}`,
  `  - match: ${quote(firstAttempt("executor", JOURNEY_LENS_TASK))}`,
  ...chunks(ATTEMPT_ONE_LINES, 1000),
  `  - match: ${quote(loopBack("executor", JOURNEY_LENS_TASK))}`,
  ...chunks(ATTEMPT_TWO_LINES, 1000),
  // Slow enough to reload the page and cancel it mid-stage.
  `  - match: ${quote(`Pipeline stage: executor\nTask: ${JOURNEY_ROOT_TASK}\n`)}`,
  ...chunks(
    Array.from({ length: 40 }, (_, i) => `root step ${i + 1}`),
    500,
  ),
  "",
].join("\n");
