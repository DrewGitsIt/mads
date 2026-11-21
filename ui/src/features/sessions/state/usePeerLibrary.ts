import { useEffect, useMemo, useState } from "react";
import { clientForHost, meshShowsAll, useMesh } from "../../../router/mesh";
import type { Agent, Workspace } from "../../../router/types";

/**
 * Adds every visible, connected peer's workspaces and agents to this
 * device's, so a remote session's card names its workspace and agent
 * instead of showing a raw id (mesh mode, docs/MESH-MODE-DESIGN.md D3b).
 * Ids are random per install, so merging by id can't mislabel; this
 * device's entry wins a tie. Read once per change of the peer set — these
 * lists are labels, not live state.
 */
export function usePeerLibrary(
  workspaces: Workspace[],
  agents: Agent[],
): { workspaces: Workspace[]; agents: Agent[] } {
  const mesh = useMesh();
  const peers = meshShowsAll(mesh)
    ? mesh.peers.filter((p) => !p.hidden && p.reachability === "connected")
    : [];
  const key = peers.map((p) => p.name).join(",");
  const [peerWorkspaces, setPeerWorkspaces] = useState<Workspace[]>([]);
  const [peerAgents, setPeerAgents] = useState<Agent[]>([]);

  useEffect(() => {
    let cancelled = false;
    if (key === "") {
      setPeerWorkspaces([]);
      setPeerAgents([]);
      return;
    }
    const clients = key.split(",").map((name) => clientForHost({ name, self: false, reachable: true }));
    void Promise.all([
      Promise.all(clients.map((c) => c.listWorkspaces().catch(() => [] as Workspace[]))),
      Promise.all(clients.map((c) => c.listAgents().catch(() => [] as Agent[]))),
    ]).then(([ws, ag]) => {
      if (cancelled) return;
      setPeerWorkspaces(ws.flat());
      setPeerAgents(ag.flat());
    });
    return () => {
      cancelled = true;
    };
  }, [key]);

  return useMemo(
    () => ({ workspaces: mergeById(workspaces, peerWorkspaces), agents: mergeById(agents, peerAgents) }),
    [workspaces, peerWorkspaces, agents, peerAgents],
  );
}

function mergeById<T extends { id: string }>(local: T[], peer: T[]): T[] {
  if (peer.length === 0) return local;
  const seen = new Set(local.map((x) => x.id));
  return [...local, ...peer.filter((x) => !seen.has(x.id))];
}
