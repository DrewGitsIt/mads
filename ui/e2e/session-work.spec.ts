import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "./fixtures";
import { createSession, importTasks, runFakePipeline } from "./lib/seed";

/**
 * Blocked: there is no Session Workspace tab switcher in the real app yet.
 * SessionsSurface.tsx mounts exactly one thing for a selected session —
 * ConversationTab — and ContextRail/TasksTab/FilesTab/PipelineTab are
 * referenced nowhere under src/features/sessions/, src/conversation/, or
 * src/App.tsx. The only place those tabs render is their standalone demo
 * mounts (tasks.html/files.html/pipeline.html), which are hardwired to
 * FakeRouterClient/FakePipelineClient fixtures, not the real Router — so
 * pointing Playwright at them would test fixtures, not the real wire. This
 * is a real missing feature (W6.2's tab registry), not a test-infra gap.
 * Un-skip once that registry lands; the seeding below already sets up a
 * session with imported tasks and a completed fake pipeline run so
 * re-enabling this spec is a diff to the assertions only.
 */
test.describe("session workspace: tasks/files/pipeline tabs", () => {
  test.fixme(true, "no Session Workspace tab switcher exists yet — see comment above");

  let sessionId: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-session-work-"));
    const session = await createSession(routerURL, { name: "e2e session-work journey" });
    sessionId = session.id;
    await importTasks(routerURL, sessionId, ["e2e session-work task"]);
    runFakePipeline({
      madsBin: process.env.MADS_BIN!,
      dataURL: process.env.MADS_DATA_URL!,
      sessionID: sessionId,
      workspace: workspaceDir,
      worktreeRoot: path.join(workspaceDir, ".worktrees"),
      env: process.env,
    });
  });

  test("tasks tab shows imported tasks", async () => {
    void sessionId;
  });

  test("files tab shows the pipeline's changed files", async () => {
    void sessionId;
  });

  test("pipeline tab shows the completed run", async () => {
    void sessionId;
  });
});
