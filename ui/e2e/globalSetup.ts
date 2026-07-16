import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { bootTopology } from "./lib/topology";
import { startFastModelFixture } from "./lib/fastModelFixture";
import { createWorkspace, setConfig, setupWorkspace } from "./lib/seed";

/**
 * Boots one shared topology for every spec except first-run.spec (which
 * needs its own scratch, pre-onboarding stack) and onboards it via direct
 * API calls rather than the wizard UI — sessions/library/dashboard/
 * settings/retro/palette specs all assume onboarding_complete already.
 */
export default async function globalSetup(): Promise<() => Promise<void>> {
  const fastModel = await startFastModelFixture();
  const topology = await bootTopology({ fastModelURL: fastModel.url });

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

  return async () => {
    await topology.teardown();
    await fastModel.close();
  };
}
