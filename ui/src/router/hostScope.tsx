import { createContext, useContext, type ReactNode } from "react";
import type { RouterClient } from "./client";
import { clientForHost } from "./mesh";
import type { SessionHost } from "./types";

/**
 * The device the session on screen lives on (docs/MESH-MODE-DESIGN.md D3b).
 * A session's own calls (/sessions/{id}/...) reach its host through the
 * Router whatever this says; what reads it are the per-host reads with no
 * session id in the path — models, provider capabilities, agents — and the
 * features that act on *this* machine (the shell dock, opening files), which
 * a remote session can't use.
 *
 * Absent (the default) means this device, so everything outside a remote
 * session's workspace behaves exactly as before mesh mode.
 */
const HostScopeContext = createContext<SessionHost | undefined>(undefined);

export function HostScopeProvider({ host, children }: { host: SessionHost | undefined; children: ReactNode }) {
  return <HostScopeContext.Provider value={host}>{children}</HostScopeContext.Provider>;
}

export function useHostScope(): SessionHost | undefined {
  return useContext(HostScopeContext);
}

/** True inside a session that lives on another device. */
export function useIsRemoteHost(): boolean {
  const host = useContext(HostScopeContext);
  return Boolean(host && !host.self);
}

/** The client for per-host reads in scope: this device's, or the peer's. */
export function useHostClient(): RouterClient {
  return clientForHost(useContext(HostScopeContext));
}
