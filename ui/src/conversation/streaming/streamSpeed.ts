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

export function streamSpeedToCharsPerSec(speed: StreamSpeed): number | null {
  return CHARS_PER_SEC[speed];
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
          if (!cancelled) setSpeed(c.stream_speed ?? "off");
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
