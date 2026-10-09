import { useEffect, useState } from "react";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";

/**
 * Settings' "Expose subagents in session list" (Config.
 * hide_delegates_in_session_list, negated), read once per mount and again
 * whenever Config changes — the same shape as useCommitSettings. Reads as
 * shown until the answer is in: that is what an unset config means.
 */
export function useShowDelegates(client: RouterClient): boolean {
  const [show, setShow] = useState(true);
  useEffect(() => {
    let cancelled = false;
    const load = () =>
      client
        .getConfig()
        .then((config) => {
          if (!cancelled) setShow(config.hide_delegates_in_session_list !== true);
        })
        .catch(() => {});
    void load();
    const unsubscribe = subscribeToEvents({ types: ["config.updated"] }, () => void load());
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [client]);
  return show;
}
