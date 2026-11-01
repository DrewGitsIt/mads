import { createContext, useContext, type ReactNode } from "react";

/**
 * The folder a relative file link in rendered markdown resolves against —
 * the session's own folder (its worktree when it has one), where the agent
 * that wrote the path was working. Undefined outside a session (task text):
 * there a relative path has nothing to resolve against and isn't a link.
 */
const LinkBaseContext = createContext<string | undefined>(undefined);

export function LinkBaseProvider({ dir, children }: { dir: string | undefined; children: ReactNode }) {
  return <LinkBaseContext.Provider value={dir}>{children}</LinkBaseContext.Provider>;
}

export function useLinkBase(): string | undefined {
  return useContext(LinkBaseContext);
}
