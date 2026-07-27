import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { StreamSpeed } from "../../router/types";

/**
 * How fast the conversation renders streamed text, decoupled from the model's
 * delivery rate. "off" is passthrough (render as it arrives); the others cap
 * the reveal rate for a steady typewriter feel. The value lives in mads config
 * (Settings → Appearance), same as theme/palette.
 */

// tokens/s targets → characters/s (~4 chars/token); null = passthrough.
const CHARS_PER_SEC: Record<StreamSpeed, number | null> = {
  off: null,
  slow: 40, // ~10 tok/s
  medium: 80, // ~20 tok/s
  fast: 120, // ~30 tok/s
};

// Total despite the Record type above lying about the domain: `speed` can
// arrive from server config as any string (including "" for a pre-field
// row — see normalizeStreamSpeed), and an out-of-domain key reads back
// `undefined` at runtime, not a type error. Coerce that to passthrough.
export function streamSpeedToCharsPerSec(speed: StreamSpeed): number | null {
  return CHARS_PER_SEC[speed] ?? null;
}

const VALID_SPEEDS = new Set<string>(["off", "slow", "medium", "fast"]);

/** Any value that isn't a recognized speed (missing field, "", unknown string) maps to "off". */
export function normalizeStreamSpeed(speed: unknown): StreamSpeed {
  return typeof speed === "string" && VALID_SPEEDS.has(speed) ? (speed as StreamSpeed) : "off";
}

/**
 * Reads the render-pacing speed from config, live — refetches on config.updated
 * so changing it in Settings takes effect in an open conversation immediately.
 */
export function useStreamSpeed(client: RouterClient): StreamSpeed {
  const [speed, setSpeed] = useState<StreamSpeed>("off");
  useEffect(() => {
    let cancelled = false;
    const load = () =>
      client
        .getConfig()
        .then((c) => {
          if (!cancelled) setSpeed(normalizeStreamSpeed(c.stream_speed));
        })
        .catch(() => {});
    void load();
    const unsubscribe = subscribeToEvents({ types: ["config.updated"] }, () => void load());
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [client]);
  return speed;
}
