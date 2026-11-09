import { useEffect, useState } from "react";
import type { RouterClient } from "../../../router/client";
import { coalesce } from "../../../router/coalesce";
import { subscribeToEvents } from "../../../router/events";
import type { DelegateFade } from "../../../router/types";

export interface DelegateSettings {
  /** Settings' "Expose subagents in session list" (Config.hide_delegates_in_session_list, negated). */
  show: boolean;
  /** Settings' "Fade out subagent cards after inactivity" (Config.delegate_fade); absent is the default. */
  fade?: DelegateFade;
}

/**
 * The session list's delegate settings, read once per mount and again
 * whenever Config changes — the same shape as useCommitSettings. Reads as
 * the defaults until the answer is in: that is what an unset config means.
 */
export function useDelegateSettings(client: RouterClient): DelegateSettings {
  const [settings, setSettings] = useState<DelegateSettings>({ show: true });
  useEffect(() => {
    let cancelled = false;
    const load = () =>
      client
        .getConfig()
        .then((config) => {
          if (cancelled) return;
          setSettings((cur) => {
            const show = config.hide_delegates_in_session_list !== true;
            const fade = config.delegate_fade;
            return cur.show === show && cur.fade === fade ? cur : { show, fade };
          });
        })
        .catch(() => {});
    // The bus replays its durable log on connect, so a fresh mount sees a
    // burst of config.updated: coalesce those into a run or two (≤ 500 ms
    // late at worst) instead of one GET /config each. The mount read is
    // immediate.
    const reload = coalesce(load);
    reload.now();
    const unsubscribe = subscribeToEvents({ types: ["config.updated"] }, () => reload.request());
    return () => {
      cancelled = true;
      reload.cancel();
      unsubscribe();
    };
  }, [client]);
  return settings;
}
