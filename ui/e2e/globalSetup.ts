import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { bootTopology } from "./lib/topology";
import { startFastModelFixture } from "./lib/fastModelFixture";
import { PIPELINE_SCENARIO } from "./lib/pipelineScenario";
import { createWorkspace, setConfig, setupWorkspace } from "./lib/seed";

/**
 * Boots one shared topology for every spec except first-run.spec (which
 * needs its own scratch, pre-onboarding stack) and onboards it via direct
 * API calls rather than the wizard UI — sessions/library/dashboard/
 * settings/retro/palette specs all assume onboarding_complete already.
 */
export default async function globalSetup(): Promise<() => Promise<void>> {
  const fastModel = await startFastModelFixture();
  const topology = await bootTopology({ fastModelURL: fastModel.url, pipelineScenario: PIPELINE_SCENARIO });

  const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-workspace-"));
  setupWorkspace(workspaceDir);
  const workspace = await createWorkspace(topology.routerURL, {
    name: "e2e workspace",
    repos: [{ path: workspaceDir, primary: true }],
  });
  await setConfig(topology.routerURL, {
    default_workspace: workspace.id,
    default_provider: "fake",
    onboarding_complete: true,
  });

  process.env.MADS_ROUTER_URL = topology.routerURL;
  process.env.MADS_DATA_URL = topology.dataURL;
  process.env.MADS_BIN = topology.madsBin;
  process.env.MADS_WORKSPACE_ID = workspace.id;
  process.env.MADS_WORKSPACE_DIR = workspaceDir;
  // runFakePipeline (called with env: process.env by specs) must resolve the
  // same $MADS_HOME the Router was booted with (-mads-home topology.homeDir,
  // above) — otherwise `mads run`'s output logs land somewhere the Pipeline
  // tab's drill-in endpoint can never find them (HANDOFF §3).
  process.env.MADS_HOME = topology.homeDir;
  // Where the services look for mergiraf (session-worktrees.spec puts a
  // stand-in there for one journey).
  process.env.MADS_MERGIRAF = topology.mergirafPath;
  // Where the services, and any `mads` CLI a spec runs, look for an agent
  // harness: an empty stand-in dir, so nothing reaches a real one.
  process.env.MADS_AGENT_BIN_DIR = topology.agentBinDir;

  return async () => {
    await topology.teardown();
    await fastModel.close();
  };
}
