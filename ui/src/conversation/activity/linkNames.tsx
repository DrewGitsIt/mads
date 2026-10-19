import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { RouterClient } from "../../router/client";
import type { Link, ToolUse } from "../../router/types";
import { toolTitle, type LinkNameResolver } from "./toolDisplay";

const LinkNamesContext = createContext<LinkNameResolver | undefined>(undefined);

export function LinkNamesProvider({
  resolve,
  children,
}: {
  resolve: LinkNameResolver;
  children: ReactNode;
}) {
  return <LinkNamesContext.Provider value={resolve}>{children}</LinkNamesContext.Provider>;
}

/** toolTitle with the surrounding conversation's linked-session names applied. */
export function useToolTitle(): (tool: ToolUse) => string {
  const resolve = useContext(LinkNamesContext);
  return (tool) => toolTitle(tool, resolve);
}

/**
 * Resolves a linked session's ID — or the ID of a link to it — to its name,
 * for titling the leader's link tool calls. Names are fetched once per
 * counterpart session; until they arrive the title falls back to a short ID.
 */
export function useLinkNames(
  client: Pick<RouterClient, "getSession">,
  sessionId: string,
  links: Link[],
): LinkNameResolver {
  const [names, setNames] = useState<Map<string, string>>(new Map());
  const others = useMemo(() => {
    const byLink = new Map<string, string>();
    for (const l of links) {
      byLink.set(
        l.id,
        l.granting_session_id === sessionId ? l.subject_session_id : l.granting_session_id,
      );
    }
    return byLink;
  }, [links, sessionId]);
  const key = [...new Set(others.values())].sort().join(",");

  useEffect(() => {
    let cancelled = false;
    for (const id of key ? key.split(",") : []) {
      client
        .getSession(id)
        .then((s) => {
          if (!cancelled) setNames((prev) => new Map(prev).set(id, s.name));
        })
        .catch(() => {});
    }
    return () => {
      cancelled = true;
    };
  }, [client, key]);

  return useMemo(
    () => (id) => names.get(others.get(id) ?? id),
    [names, others],
  );
}
