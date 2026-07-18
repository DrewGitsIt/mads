import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

export interface SeedSession {
  id: string;
  name: string;
  addr: string;
}

export async function createSession(
  routerURL: string,
  body: { name: string; provider?: string; workspace?: string },
): Promise<SeedSession> {
  const res = await fetch(`${routerURL}/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`createSession: status ${res.status}`);
  const data = (await res.json()) as { session: { id: string; name: string }; addr: string };
  return { id: data.session.id, name: data.session.name, addr: data.addr };
}

/**
 * Writes a session straight to the Data Service, bypassing Router's
 * POST /sessions (which always spawns a child immediately,
 * handleCreateSession, services/router/internal/api/sessions.go). Used to
 * seed a session that genuinely never had a live child — the "sending a
 * message wakes a dormant session" journey needs this starting state, which
 * the UI itself can't reach (Launchpad always auto-sends the goal on
 * create).
 */
export async function createSessionNoSpawn(
  dataURL: string,
  body: { name: string; provider: string; workspace?: string },
): Promise<{ id: string; name: string }> {
  const res = await fetch(`${dataURL}/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    // activity: "inactive" mirrors the steady state a real never-spawned
    // session settles into after Router's boot sweep (ReconcileOrphanedSessions)
    // — the Data Service store defaults a bare create to "active"
    // (services/data/internal/store/sessions.go), which would otherwise
    // suppress ConversationTab's session.activity-gated waking indicator
    // even though the Router genuinely has to wake it on send.
    body: JSON.stringify({ ...body, activity: "inactive" }),
  });
  if (!res.ok) throw new Error(`createSessionNoSpawn: status ${res.status}`);
  const data = (await res.json()) as { id: string; name: string };
  return { id: data.id, name: data.name };
}

export async function importTasks(
  routerURL: string,
  sessionID: string,
  titles: string[],
): Promise<{ id: string; title: string }[]> {
  const res = await fetch(`${routerURL}/tasks/import`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      tasks: titles.map((title) => ({ session_id: sessionID, title, source: "user" })),
    }),
  });
  if (!res.ok) throw new Error(`importTasks: status ${res.status}`);
  const data = (await res.json()) as { tasks: { id: string; title: string }[] };
  return data.tasks;
}

/** Full GET→merge→PUT round-trip: no separate deps endpoint, dependencies live on Task.dependencies (PUT /tasks/{id} full-replace). */
export async function linkDependency(
  routerURL: string,
  taskId: string,
  dependsOnId: string,
): Promise<void> {
  const getRes = await fetch(`${routerURL}/tasks/${taskId}`);
  if (!getRes.ok) throw new Error(`getTask: status ${getRes.status}`);
  const task = await getRes.json();
  const putRes = await fetch(`${routerURL}/tasks/${taskId}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...task, dependencies: [...(task.dependencies ?? []), dependsOnId] }),
  });
  if (!putRes.ok) throw new Error(`linkDependency: status ${putRes.status}`);
}

/**
 * Records a file change directly against the Data Service. Nothing in the
 * pipeline/provider stack calls RecordFileChange yet (file-change tracking
 * has no producer), and the Router only exposes GET /sessions/{id}/files —
 * so seeding a change for a fake-pipeline session means POSTing straight to
 * the Data Service, same as importTasks seeds tasks via the Router.
 */
export async function recordFileChange(
  dataURL: string,
  sessionID: string,
  path: string,
): Promise<void> {
  const res = await fetch(`${dataURL}/sessions/${sessionID}/files`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Mads-Actor": "e2e" },
    body: JSON.stringify({ session_id: sessionID, path, change: "added", origin: "tool_use" }),
  });
  if (!res.ok) throw new Error(`recordFileChange: status ${res.status}`);
}

/**
 * Makes a real commit in repoDir (the same repo setupWorkspace initialized)
 * and reports it to the Data Service with repo=repoDir, so the Router's
 * git-enrichment (services/router/internal/api/commits.go) can resolve a
 * real file list for it — same "seed the producer gap directly" pattern as
 * recordFileChange (nothing in the pipeline/provider stack makes a real
 * pipeline-stage commit against the fake provider today).
 */
export async function recordRealCommit(
  dataURL: string,
  sessionID: string,
  repoDir: string,
  opts: { path: string; content: string; message: string; taskId: string },
): Promise<string> {
  const filePath = path.join(repoDir, opts.path);
  mkdirSync(path.dirname(filePath), { recursive: true });
  writeFileSync(filePath, opts.content);
  const run = (args: string[]) => execFileSync("git", args, { cwd: repoDir, stdio: "pipe" });
  run(["add", "-A"]);
  run(["-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", "commit", "-q", "-m", opts.message]);
  const hash = run(["rev-parse", "HEAD"]).toString().trim();
  const res = await fetch(`${dataURL}/sessions/${sessionID}/commits`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Mads-Actor": "e2e" },
    body: JSON.stringify({
      session_id: sessionID,
      hash,
      message: opts.message,
      repo: repoDir,
      trailer_task_id: opts.taskId,
    }),
  });
  if (!res.ok) throw new Error(`recordRealCommit: status ${res.status}`);
  return hash;
}

export interface WorkspaceRepo {
  path: string;
  primary?: boolean;
}

export async function createWorkspace(
  routerURL: string,
  body: { name: string; repos?: WorkspaceRepo[]; defaults?: Record<string, string> },
): Promise<{ id: string; name: string }> {
  const res = await fetch(`${routerURL}/workspaces`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...body, defaults: body.defaults ?? {} }),
  });
  if (!res.ok) throw new Error(`createWorkspace: status ${res.status}`);
  return (await res.json()) as { id: string; name: string };
}

/** Full GET→merge→PUT round-trip: handleUpdateConfig requires a complete contracts.Config, not a partial patch. */
export async function setConfig(
  routerURL: string,
  patch: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const getRes = await fetch(`${routerURL}/config`);
  if (!getRes.ok) throw new Error(`getConfig: status ${getRes.status}`);
  const current = (await getRes.json()) as Record<string, unknown>;
  const merged = { ...current, ...patch };
  const putRes = await fetch(`${routerURL}/config`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(merged),
  });
  if (!putRes.ok) throw new Error(`updateConfig: status ${putRes.status}`);
  return (await putRes.json()) as Record<string, unknown>;
}

/** Writes a trivial Makefile and git-inits dir so the default template's build/test/merge stages succeed, mirroring scripts/e2esmoke/main_test.go's setupWorkspace. */
export function setupWorkspace(dir: string): void {
  mkdirSync(dir, { recursive: true });
  writeFileSync(dir + "/Makefile", "build:\n\t@true\ntest:\n\t@true\n");
  const run = (args: string[]) => execFileSync("git", args, { cwd: dir, stdio: "pipe" });
  run(["init", "-q", "-b", "main", "."]);
  run(["-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", "add", "."]);
  run(["-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", "commit", "-q", "-m", "init"]);
  run(["config", "merge.defaultToUpstream", "true"]);
  run(["config", "branch.main.remote", "."]);
  run(["config", "branch.main.merge", "refs/heads/main"]);
}

/** Runs a scripted pipeline over providers/fake via the built `mads run` CLI, mirroring scripts/e2esmoke/main_test.go's step 7. */
export function runFakePipeline(opts: {
  madsBin: string;
  dataURL: string;
  sessionID: string;
  workspace: string;
  worktreeRoot: string;
  env: NodeJS.ProcessEnv;
}): void {
  mkdirSync(opts.worktreeRoot, { recursive: true });
  execFileSync(
    opts.madsBin,
    [
      "run",
      "-data",
      opts.dataURL,
      "-session",
      opts.sessionID,
      "-workspace",
      opts.workspace,
      "-provider",
      "fake",
      "-stage-timeout",
      "30s",
      "-worktree-root",
      opts.worktreeRoot,
    ],
    { env: opts.env, stdio: "pipe" },
  );
}

/** POST /retros — kicks off async DPA generation against fastModelFixture. */
export async function startRetro(routerURL: string, sessionIds: string[]): Promise<string> {
  const res = await fetch(`${routerURL}/retros`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_ids: sessionIds }),
  });
  if (res.status !== 202) throw new Error(`startRetro: status ${res.status}`);
  const data = (await res.json()) as { retro_id: string };
  return data.retro_id;
}

export interface SeedRetroDPA {
  id: string;
  problem: string;
  evidence: string;
  root_cause: string;
  remediation_layer: string;
  suggested_action: string;
  status: string;
}

export interface SeedRetro {
  id: string;
  session_ids: string[];
  dpas: SeedRetroDPA[];
}

/** Polls GET /retros/{id} until generation completes, mirroring e2esmoke's retro poll loop. */
export async function pollRetro(
  routerURL: string,
  retroId: string,
  timeoutMs = 30_000,
): Promise<SeedRetro> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const res = await fetch(`${routerURL}/retros/${retroId}`);
    if (res.ok) return (await res.json()) as SeedRetro;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`pollRetro: ${retroId} did not complete in time`);
}

/**
 * POST /sessions/{id}/messages — drives a real chat turn against a running
 * (fake-provider) session, mirroring what Composer.tsx sends. Used to
 * trigger the real digest pipeline (session.turn_ended -> digestwatch ->
 * job.GenerateDigest), not a shortcut around it (linked-sessions.spec.ts,
 * ui/src/features/sessions/HANDOFF.md decision D7).
 */
export async function sendChatMessage(
  routerURL: string,
  sessionID: string,
  text: string,
): Promise<void> {
  const res = await fetch(`${routerURL}/sessions/${sessionID}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text }),
  });
  if (!res.ok) throw new Error(`sendChatMessage: status ${res.status}`);
}

/** POST /links/{id}/inject — the "inject via the API" step of the linked-sessions journey. */
export async function injectLinkMessage(
  routerURL: string,
  linkID: string,
  text: string,
): Promise<void> {
  const res = await fetch(`${routerURL}/links/${linkID}/inject`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text }),
  });
  if (!res.ok) throw new Error(`injectLinkMessage: status ${res.status}`);
}

/**
 * Polls GET /sessions/{id}/transcript for a message whose text contains
 * substring — decouples "wait for the async digest pipeline
 * (session.turn_ended -> digestwatch -> job.GenerateDigest -> FastModel ->
 * deliverDigest)" from the UI assertion that the digest card renders,
 * mirroring pollRetro's approach to the retro pipeline.
 */
export async function pollTranscriptContains(
  routerURL: string,
  sessionID: string,
  textSubstring: string,
  timeoutMs = 20_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const res = await fetch(`${routerURL}/sessions/${sessionID}/transcript`);
    if (res.ok) {
      const messages = (await res.json()) as { text?: string }[];
      if (messages.some((m) => m.text?.includes(textSubstring))) return;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`pollTranscriptContains: ${sessionID} transcript never contained "${textSubstring}"`);
}
