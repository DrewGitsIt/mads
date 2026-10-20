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
/** tasks-pipeline.spec: left out of the first run, then the slow task the second run is stopped under. */
export const JOURNEY_ROOT_TASK = "tp journey: left-out root";

/**
 * tasks-stop-pickup.spec: its executor announces a session ref (so a later
 * run can resume that session), leaves a file in the worktree, and streams
 * slowly enough to Stop mid-turn.
 */
export const STOP_PICKUP_TASK = "sp journey: resumable task";
export const STOP_PICKUP_FILE = "sp-interrupted-work.txt";
export const STOP_PICKUP_LINES = Array.from({ length: 60 }, (_, i) => `sp first-run step ${i + 1}`);
/** What the resumed executor says: proof the run resumed the session rather than starting a new one. */
export const RESUMED_LINES = Array.from({ length: 6 }, (_, i) => `sp resumed step ${i + 1}`);
/** The engine's prompt for a resumed agent session (services/pipeline/engine/progress.go resumePrompt). */
const RESUME_PROMPT = "You were interrupted; continue where you left off.";

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
  // Slow enough to reload the page and stop the run mid-stage.
  `  - match: ${quote(`Pipeline stage: executor\nTask: ${JOURNEY_ROOT_TASK}\n`)}`,
  ...chunks(
    Array.from({ length: 40 }, (_, i) => `root step ${i + 1}`),
    500,
  ),
  // A resumed one-shot (OneShotRequest.Resume) gets the resume prompt, not
  // the task brief, so this can't key on a title: only tasks-stop-pickup
  // resumes a session in this suite.
  `  - match: ${quote(RESUME_PROMPT)}`,
  "    resumed: true",
  ...chunks(RESUMED_LINES, 1000),
  `  - match: ${quote(`Pipeline stage: executor\nTask: ${STOP_PICKUP_TASK}\n`)}`,
  "    announce_ref: true",
  "    tools:",
  "      - name: write_file",
  `        path: ${quote(STOP_PICKUP_FILE)}`,
  `        content: ${quote("half done\n")}`,
  ...chunks(STOP_PICKUP_LINES, 500),
  "",
].join("\n");
