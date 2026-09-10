import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import {
  createSession,
  importTasks,
  linkDependency,
  recordFileChange,
  recordRealCommit,
  runFakePipeline,
  setupWorkspace,
} from "./lib/seed";

const changedFilePath = "src/example.ts";
const commitMessage = "e2e: seed a real pipeline commit";

test.describe("session workspace: tasks/files/pipeline tabs", () => {
  test.describe.configure({ mode: "serial" });

  let sessionId: string;
  let sessionName: string;
  let workTask: { id: string; title: string };
  let depTask: { id: string; title: string };
  let dependentTask: { id: string; title: string };
  let secondRunTask: { id: string; title: string };
  let commitHash: string;
  // The two pipeline runs' own run_seq (== their pipeline.started event's
  // seq), oldest first — seeded so the "run picker replays historical runs"
  // journey below has two genuinely distinct runs to switch between.
  let firstRunSeq: number;
  let secondRunSeq: number;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-session-work-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e session-work journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    sessionId = session.id;
    [workTask] = await importTasks(routerURL, sessionId, ["e2e session-work task"]);
    [depTask, dependentTask] = await importTasks(routerURL, sessionId, [
      "e2e graph dep",
      "e2e graph dependent",
    ]);
    await linkDependency(routerURL, dependentTask.id, depTask.id);
    const runFakePipelineOpts = {
      madsBin: process.env.MADS_BIN!,
      dataURL,
      sessionID: sessionId,
      workspace: workspaceDir,
      worktreeRoot: path.join(workspaceDir, ".worktrees"),
      env: process.env,
    };
    runFakePipeline(runFakePipelineOpts);
    await recordFileChange(dataURL, sessionId, changedFilePath);
    commitHash = await recordRealCommit(dataURL, sessionId, workspaceDir, {
      path: "src/commit-file.ts",
      content: "export const x = 1;\n",
      message: commitMessage,
      taskId: workTask.id,
    });

    // A second, independent run (a fresh task, no dependency on anything
    // above) — dependentTask stays untouched (still blocked on depTask),
    // so this doesn't disturb the graph-mode journey below.
    [secondRunTask] = await importTasks(routerURL, sessionId, ["e2e second pipeline run task"]);
    runFakePipeline(runFakePipelineOpts);

    const runsRes = await fetch(`${routerURL}/metrics/pipelines?session_id=${sessionId}`);
    if (!runsRes.ok) throw new Error(`metricsPipelineRuns: status ${runsRes.status}`);
    const runs = (await runsRes.json()) as { run_seq: number }[];
    if (runs.length !== 2) throw new Error(`expected 2 seeded pipeline runs, got ${runs.length}`);
    [firstRunSeq, secondRunSeq] = [...runs].map((r) => r.run_seq).sort((a, b) => a - b);
  });

  test("tasks tab shows imported tasks", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Tasks" }).click();
    // Scoped to the Tasks panel: the conversation panel's context rail also
    // renders TaskRow copies (hidden while collapsed), which an unscoped
    // getByText would collide with under strict mode.
    await expect(
      page.locator("#workspace-panel-tasks").getByText("e2e session-work task"),
    ).toBeVisible();
  });

  // Reproduces tsk_15aa8fee6de66967's blocker: with the rail and the Tasks
  // tab both mounted (each a live SSE subscriber), a task Save PUT used to
  // queue behind a saturated HTTP/1.1 connection pool and hang indefinitely.
  // The tight timeout below fails loudly instead of just eventually timing
  // out at Playwright's default if that regresses.
  test("editing and saving a task from a loaded workspace completes promptly", async ({
    page,
    routerURL,
  }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks" }).click();

    await page.locator("#workspace-panel-tasks").getByText(workTask.title, { exact: true }).click();
    await page.getByLabel("Title").fill(`${workTask.title} (edited)`);
    await page.getByRole("button", { name: "Save" }).click();

    // handleSave doesn't close the sheet — the Save button toggling back
    // from "Saving…" is the only post-save UI signal, and it must happen
    // promptly, not after Playwright's default (much longer) timeout.
    await expect(page.getByRole("button", { name: "Saving…" })).toHaveCount(0, { timeout: 5000 });
    await expect(page.getByLabel("Title")).toHaveValue(`${workTask.title} (edited)`);
  });

  test("files tab shows the pipeline's changed files", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Files" }).click();
    await expect(page.getByText(changedFilePath)).toBeVisible();
    await expect(page.getByText(commitMessage)).toBeVisible();
    await expect(page.getByText(commitHash.slice(0, 7))).toBeVisible();
    // Scoped to the Files panel (the conversation panel's context rail holds
    // a hidden TaskRow copy of this title) and .first(): within Files the
    // title legitimately recurs — two attribution chips plus the pipeline
    // auto-commit message all contain it.
    await expect(
      page.locator("#workspace-panel-files").getByText(workTask.title).first(),
    ).toBeVisible();

    // The seeded commit's file (src/commit-file.ts, added against a real
    // parent commit that didn't contain it — recordRealCommit) opens its
    // diff on click, exercising the endpoint's added-file/empty-original path.
    await page.getByText("src/commit-file.ts").click();
    // .monaco-diff-editor is the DiffEditor widget's own top-level container —
    // unlike .monaco-editor, which Monaco also applies to several nested
    // internal panes/gutters, so it isn't reliably "the" visible editor root.
    await expect(page.locator(".monaco-diff-editor").first()).toBeVisible();
    await expect(page.getByText(`Diff from commit ${commitHash.slice(0, 7)}`)).toBeVisible();
  });

  test("pipeline tab shows the completed run", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Pipeline" }).click();
    // Scoped to the Pipeline panel: the hidden Tasks panel and the
    // conversation panel's context rail carry "Done" status chips too.
    const doneColumn = page
      .locator("#workspace-panel-pipeline")
      .getByText("Done", { exact: true })
      .locator("../..");
    await expect(doneColumn.getByText("e2e session-work task")).toBeVisible();

    // The run finished before this page load. The Data Service's SSE stream
    // replays a session's full history on connect (since=0 by default), so
    // usePipelineRun's live fold catches this run on its own — the header
    // must show its finished outcome, never "idle" (the bug this task fixes).
    // (A session with more history than a fresh SSE connection replays before
    // this assertion's timeout would instead fall back to the historical fetch
    // path and its RunPicker — both are covered at the unit level.)
    await expect(page.getByText("idle", { exact: true })).not.toBeVisible();
    await expect(page.getByText("completed", { exact: true })).toBeVisible();
  });

  test("pipeline tab run picker replays historical runs", async ({ page, routerURL }) => {
    // The test above documents that a fresh SSE connection usually replays
    // its full backlog before the historical fetch resolves, so the live
    // fold wins the race and the RunPicker never mounts. Stall the SSE
    // request indefinitely (never continue/fulfill/abort it) to force the
    // opposite, deterministically, without depending on backlog size:
    // usePipelineRun's phase stays "idle" for the whole test, so
    // usePipelineTab always renders the historical (event-log replay) path.
    await page.route("**/events*", async (route) => {
      const accept = route.request().headers()["accept"] ?? "";
      if (accept.includes("text/event-stream")) return; // hang forever
      await route.continue();
    });

    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    const trigger = page.getByRole("button", { name: "Run history" });
    await expect(trigger).toBeVisible();
    const initialLabel = await trigger.textContent();

    await trigger.click();
    const options = page.getByRole("button", { name: /Run · started/ });
    await expect(options).toHaveCount(2);

    // Selecting the older run must fetch and fold *that* run's own event
    // window from the real backend — not just flip a client-side label.
    const [response] = await Promise.all([
      page.waitForResponse(
        (res) => res.url().includes("/events") && res.url().includes(`since=${firstRunSeq - 1}`),
      ),
      // Newest run is listed/selected first — click the second (older) option.
      options.nth(1).click(),
    ]);
    expect(response.ok()).toBeTruthy();

    await expect(trigger).not.toHaveText(initialLabel ?? "");
  });

  test("graph mode shows the unsequenced tray and a canvas node", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks" }).click();
    await page.getByRole("button", { name: "Graph", exact: true }).click();

    await expect(page.getByText("Runs when a slot opens", { exact: false })).toBeVisible();
    await expect(
      page.locator("#workspace-panel-tasks").getByText("e2e session-work task"),
    ).toBeVisible(); // tray
    await expect(page.locator(".react-flow__node").getByText("e2e graph dependent")).toBeVisible(); // canvas

    // All 4 seeded tasks end up "done" once both scripted pipeline runs
    // finish — the status-filter strip's live counts must reflect that.
    await expect(page.getByRole("button", { name: "4 done" })).toBeVisible();
    await expect(page.getByRole("button", { name: "0 in progress" })).toBeVisible();
    await expect(page.getByRole("button", { name: "0 ready" })).toBeVisible();
    await expect(page.getByRole("button", { name: "0 blocked" })).toBeVisible();

    // The seeded workTask ended "done" and lives in the tray (no deps) — it
    // must visibly recede there too, same as a done canvas card would.
    const workTaskCard = page
      .locator("#workspace-panel-tasks")
      .getByText(workTask.title)
      .locator("..");
    await expect(workTaskCard).toHaveCSS("opacity", "0.55");
  });

  test("dependencies pane: statuses visible, add via picker, save, edge shows on the graph", async ({
    page,
    routerURL,
  }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks" }).click();

    await page
      .locator("#workspace-panel-tasks")
      .getByText(dependentTask.title, { exact: true })
      .click();
    // Scoped to the open detail sheet — depTask/secondRunTask's own list-view
    // rows sit behind it in the DOM with the same title text.
    const sheet = page.getByRole("complementary");
    await expect(sheet.getByText(depTask.title, { exact: true })).toBeVisible();
    await expect(sheet.getByText(/^done · landed/)).toBeVisible();
    await expect(sheet.getByText("1 · 1 done")).toBeVisible();

    await sheet.getByText("+ Add dependency…").click();
    await sheet.getByPlaceholder("Search tasks…").fill(secondRunTask.title);
    await sheet.getByText(secondRunTask.title, { exact: true }).click();
    await page.keyboard.press("Escape");

    await expect(sheet.getByText(secondRunTask.title, { exact: true })).toBeVisible();
    await expect(sheet.getByText("2 · 2 done")).toBeVisible();

    await sheet.getByRole("button", { name: "Save" }).click();
    await expect(sheet.getByRole("button", { name: "Saving…" })).toHaveCount(0, { timeout: 5000 });

    // Both dependency rows must still be there after the save round-trips.
    await expect(sheet.getByText(depTask.title, { exact: true })).toBeVisible();
    await expect(sheet.getByText(secondRunTask.title, { exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Graph", exact: true }).click();
    await expect(
      page.locator(`.react-flow__edge[data-id="${secondRunTask.id}->${dependentTask.id}"]`),
    ).toBeVisible();
  });
});

test.describe("session workspace: blocked task review from Tasks tab", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e blocked task";
  let sessionName: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-blocked-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e blocked task journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    await importTasks(routerURL, session.id, [taskTitle]);

    // Fails the default template's code_review stage every time (mirrors
    // services/pipeline/engine/engine_test.go's TestDefaultTemplateReviewLoopBack)
    // so the task loops back max_loops(2) times, then blocks. Every other
    // stage's prompt falls through to the catch-all and passes.
    const scenarioPath = path.join(workspaceDir, "scenario.yaml");
    writeFileSync(
      scenarioPath,
      [
        "name: blocked-review",
        "rules:",
        `  - match: "code_review\\nTask: ${taskTitle}"`,
        '    reply: "This won\'t do.\\nSTAGE: fail — missing error handling"',
        '  - match: ""',
        '    reply: "STAGE: pass"',
        "",
      ].join("\n"),
    );

    try {
      runFakePipeline({
        madsBin: process.env.MADS_BIN!,
        dataURL,
        sessionID: session.id,
        workspace: workspaceDir,
        worktreeRoot: path.join(workspaceDir, ".worktrees"),
        env: process.env,
        scenarioPath,
      });
    } catch {
      // cmd/mads/run.go returns a non-zero exit whenever a run ends with a
      // blocked task — exactly the outcome this scenario seeds, not a
      // genuine seeding failure.
    }
  });

  test("Tasks tab: blocked task's detail sheet shows the blocker note and a working Retry", async ({
    page,
    routerURL,
  }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks" }).click();

    // Same Tasks-panel scoping as above: the context rail's hidden TaskRow
    // copy of this title would otherwise be the first strict-mode match.
    await page.locator("#workspace-panel-tasks").getByText(taskTitle, { exact: true }).click();
    const sheet = page.getByRole("complementary");
    await expect(sheet.getByRole("combobox")).toHaveValue("blocked");
    // The blocker note is intentionally duplicated: once in the Pipeline run
    // section's BlockedTaskFeedback, once in the generic notes list further
    // down (TaskDetailSheet.test.tsx) — assert at least one is visible.
    await expect(sheet.getByText(/code_review/).first()).toBeVisible();

    const retryButton = sheet.getByRole("button", { name: "Retry" });
    await expect(retryButton).toBeVisible();
    await retryButton.click();

    // The retried task's task.status_changed event drives useTaskStore's
    // live refetch, which flips the sheet's Status field out of "blocked".
    await expect(sheet.getByRole("combobox")).not.toHaveValue("blocked");
  });
});
