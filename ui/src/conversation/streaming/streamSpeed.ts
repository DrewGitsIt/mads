import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { StreamSpeed } from "../../router/types";

/**
 * How fast the conversation reveals streamed text, decoupled from the
 * model's delivery rate. "off" (Native) is passthrough — render as it
 * arrives; the others are a reading pace in words per minute, held even when
 * the agent has already finished (a burst plays out at that pace instead of
 * landing at once). Slow and fast are presets; custom reads `stream_wpm`.
 * The value lives in mads config (Settings → Appearance), same as
 * theme/palette.
 */

export const STREAM_WPM_PRESETS = { slow: 250, fast: 1500 } as const;
/** Custom's pace before the user has typed one. */
export const DEFAULT_CUSTOM_WPM = 350;
// contracts.MinStreamWPM / MaxStreamWPM.
export const MIN_STREAM_WPM = 60;
export const MAX_STREAM_WPM = 2000;

export interface StreamPace {
  speed: StreamSpeed;
  /** Custom's words per minute, when set. */
  wpm?: number;
}

/**
 * Words per minute for a pace; null = passthrough. Total: an out-of-domain
 * speed (a value this build doesn't know) is passthrough, never a NaN that
 * would reach the pacer (the 2026-07-27 freeze).
 */
export function streamPaceToWpm({ speed, wpm }: StreamPace): number | null {
  switch (speed) {
    case "slow":
      return STREAM_WPM_PRESETS.slow;
    case "fast":
    case "medium": // retired; read as fast
      return STREAM_WPM_PRESETS.fast;
    case "custom":
      return clampWpm(wpm);
    default:
      return null;
  }
}

/** A usable custom pace: unset or garbage → the default, else clamped to the contract's bounds. */
export function clampWpm(wpm: unknown): number {
  if (typeof wpm !== "number" || !Number.isFinite(wpm) || wpm <= 0) return DEFAULT_CUSTOM_WPM;
  return Math.round(Math.min(MAX_STREAM_WPM, Math.max(MIN_STREAM_WPM, wpm)));
}

const VALID_SPEEDS = new Set<string>(["off", "slow", "fast", "custom"]);

/**
 * Any value that isn't a recognized speed (missing field, "", unknown
 * string) maps to "off"; the retired "medium" maps to "fast".
 */
export function normalizeStreamSpeed(speed: unknown): StreamSpeed {
  if (speed === "medium") return "fast";
  return typeof speed === "string" && VALID_SPEEDS.has(speed) ? (speed as StreamSpeed) : "off";
}

/**
 * Reads the reveal pace from config, live — refetches on config.updated so
 * changing it in Settings takes effect in an open conversation immediately.
 */
export function useStreamSpeed(client: RouterClient): StreamPace {
  const [pace, setPace] = useState<StreamPace>({ speed: "off" });
  useEffect(() => {
    let cancelled = false;
    const load = () =>
      client
        .getConfig()
        .then((c) => {
          if (cancelled) return;
          const speed = normalizeStreamSpeed(c.stream_speed);
          const wpm = c.stream_wpm;
          setPace((cur) => (cur.speed === speed && cur.wpm === wpm ? cur : { speed, wpm }));
        })
        .catch(() => {});
    void load();
    const unsubscribe = subscribeToEvents({ types: ["config.updated"] }, () => void load());
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [client]);
  return pace;
}
