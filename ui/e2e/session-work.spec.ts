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

test.describe("session workspace: tasks and changes tabs", () => {
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

    // The tab shows the session's last run, where a row click zooms into the
    // task; the row's ✎ opens its detail sheet.
    await page.locator("#workspace-panel-tasks").getByRole("button", { name: `Edit ${workTask.title}` }).click();
    await page.getByLabel("Title").fill(`${workTask.title} (edited)`);
    await page.getByRole("button", { name: "Save" }).click();

    // handleSave doesn't close the sheet — the Save button toggling back
    // from "Saving…" is the only post-save UI signal, and it must happen
    // promptly, not after Playwright's default (much longer) timeout.
    await expect(page.getByRole("button", { name: "Saving…" })).toHaveCount(0, { timeout: 5000 });
    await expect(page.getByLabel("Title")).toHaveValue(`${workTask.title} (edited)`);
  });

  test("changes tab shows the pipeline's changed files", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Changes" }).click();
    // The Changes tab (formerly Files; panel id unchanged) lists what
    // differs on disk, not the touch ledger — so changedFilePath, a ledger
    // record with no file behind it, isn't listed. Commits are a grouping.
    const changes = page.locator("#workspace-panel-files");
    await changes.getByRole("radio", { name: "Commit" }).click();
    await expect(changes.getByText(commitMessage)).toBeVisible();
    await expect(changes.getByText(commitHash.slice(0, 7)).first()).toBeVisible();

    // The seeded commit's file (src/commit-file.ts, added against a real
    // parent commit that didn't contain it — recordRealCommit) opens its
    // diff on click, exercising the endpoint's added-file/empty-original path.
    await changes.getByText("commit-file.ts").first().click();
    // An added file has nothing to compare against, so it opens as plain
    // code under a "New file" banner. The diff editor stays mounted but
    // hidden (DiffViewer's DiffBody), so check the plain editor is the one showing.
    await expect(changes.getByText(/^New file · \d+ lines?$/)).toBeVisible();
    await expect(changes.locator(".monaco-editor:not(.monaco-diff-editor *)").first()).toBeVisible();
    await expect(changes.locator(".monaco-diff-editor").first()).toBeHidden();
    await expect(page.getByText(`Diff from commit ${commitHash.slice(0, 7)}`)).toBeVisible();
  });

  test("tasks tab shows the completed run", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();

    // Both runs finished before this page load: the run bar shows the newest
    // one with its outcome, and the metro has the run's tasks at Done.
    const panel = page.locator("#workspace-panel-tasks");
    const runTrigger = panel.locator('button[aria-haspopup="menu"]').first();
    await expect(runTrigger).toContainText("Run 2");
    await expect(runTrigger).toContainText("completed");
    await expect(panel.getByRole("button", { name: "New run", exact: true })).toBeVisible();
    // The list's Done group (the metro's Done stack collapses past 3 tokens).
    await expect(
      panel.getByRole("region", { name: /^Done/ }).locator(`li[data-task="${secondRunTask.id}"]`),
    ).toBeVisible();
  });

  test("the run menu replays an earlier run", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();

    const panel = page.locator("#workspace-panel-tasks");
    const runTrigger = panel.locator('button[aria-haspopup="menu"]').first();
    await expect(runTrigger).toContainText("Run 2");

    // In Run 2, workTask (done in Run 1) has nothing of its own to show.
    await panel.locator(`li[data-task="${workTask.id}"]`).getByRole("button").first().click();
    const lens = panel.getByTestId("task-lens");
    await expect(lens.getByText("Not started.")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(lens).toBeHidden();

    await runTrigger.click();
    const menu = page.getByRole("menu", { name: "Runs" });
    await expect(menu.getByRole("menuitemradio", { name: /^Run \d/ })).toHaveCount(2);

    // Picking the older run must fetch and fold *that* run's own event
    // window from the real backend — not just flip a client-side label.
    const [response] = await Promise.all([
      page.waitForResponse(
        (res) => res.url().includes("/events") && res.url().includes(`since=${firstRunSeq - 1}`),
      ),
      menu.getByRole("menuitemradio", { name: /^Run 1/ }).click(),
    ]);
    expect(response.ok()).toBeTruthy();
    await expect(runTrigger).toContainText("Run 1");

    // Run 1's fold has workTask's stages.
    await panel.locator(`li[data-task="${workTask.id}"]`).getByRole("button").first().click();
    await expect(lens.getByTestId("stage-timeline-entry").first()).toBeVisible();
    await expect(lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "verify: done" })).toBeVisible();
    expect(secondRunSeq).toBeGreaterThan(firstRunSeq);
  });

  test("graph mode shows the unsequenced tray and a canvas node", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks" }).click();
    // Plan: the task-status filter strip is a planning tool (a live or past
    // run's graph speaks in run statuses and hides it).
    await page.locator("#workspace-panel-tasks").getByRole("button", { name: "New run", exact: true }).click();
    await page.getByRole("radio", { name: "Graph" }).click();

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
      .getByRole("button", { name: `Edit ${dependentTask.title}` })
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

    await page.getByRole("radio", { name: "Graph" }).click();
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
    // copy of this title would otherwise be the first strict-mode match. The
    // tab shows the run that blocked it: the row zooms into the task's lens,
    // and Task details opens the sheet.
    const panel = page.locator("#workspace-panel-tasks");
    await panel.locator("li[data-task]").getByRole("button", { name: taskTitle, exact: true }).click();
    await panel.getByTestId("task-lens").getByRole("button", { name: "Task details" }).click();
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
