import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "./fixtures";
import { createSession, importTasks, runFakePipeline } from "./lib/seed";

/**
 * DESCOPE DECISION (executor, this task): this journey stays unimplemented.
 *
 * Not a test-infra gap — a real, missing product feature. SessionsSurface.tsx
 * mounts exactly one thing for a selected session (ConversationTab); there is
 * no Session Workspace tab switcher anywhere in src/App.tsx or
 * src/features/sessions/. TasksTab/FilesTab/PipelineTab/ContextRail exist
 * only as standalone components with their own demo-only mounts
 * (tasks.html/files.html/pipeline.html), never referenced from the real app
 * shell. Confirmed by grep across ui/src — the only non-test, non-demo
 * references to those four components are the demo mounts themselves.
 *
 * Building the tab registry that would make this journey real is net-new UI
 * architecture (navigation state, a tab bar, wiring three already-built
 * components with real routerClient/pipelineClient props into the session
 * pane) — out of proportion for "ui-smoke: add a Playwright journey suite."
 * Contrast with sessions.spec's provider-reply journey, which was blocked on
 * a two-route proxy gap (POST /sessions/{id}/messages, /interrupt) small
 * enough to fix in this same task — see route_manifest_test.go's
 * routeManifest/knownGapRoutes split for the line between "small, proxy the
 * existing child endpoint" and "build a feature," which is the line this
 * journey falls on the wrong side of.
 *
 * Requires a pipeline decision: either a follow-up task builds the Session
 * Workspace tab registry (tracked nowhere formally yet — this comment is
 * that tracking until one exists), or the acceptance bar for this task is
 * amended to seven journeys. The seeding below is left in place so
 * un-skipping is a diff to the assertions only once the registry lands.
 */
test.describe("session workspace: tasks/files/pipeline tabs", () => {
  test.skip(true, "no Session Workspace tab switcher exists in the app yet — see DESCOPE DECISION above");

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
