import { useSyncExternalStore } from "react";

/**
 * How fast the conversation renders streamed text, decoupled from the model's
 * delivery rate. "off" is passthrough (render as it arrives — today's default);
 * the others cap the reveal rate for a steady typewriter feel.
 *
 * Stored client-side (this is a pure render preference). It's intentionally a
 * localStorage setting for now — promoting it to a synced mads config field is
 * a follow-up once a Settings surface exists.
 */
export type StreamSpeed = "off" | "slow" | "medium" | "fast";

export const STREAM_SPEEDS: StreamSpeed[] = ["off", "slow", "medium", "fast"];

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

const KEY = "mads:stream-speed";

function read(): StreamSpeed {
  try {
    const v = localStorage.getItem(KEY);
    if (v && (STREAM_SPEEDS as string[]).includes(v)) return v as StreamSpeed;
  } catch {
    // no localStorage (SSR/tests) — fall through to the default
  }
  return "off";
}

// Module-level store so every mounted consumer stays in sync when the setting
// changes (and across tabs via the storage event), without a provider.
const listeners = new Set<() => void>();
let current: StreamSpeed = read();

export function setStreamSpeed(speed: StreamSpeed): void {
  current = speed;
  try {
    localStorage.setItem(KEY, speed);
  } catch {
    // ignore — the in-memory value still drives this session
  }
  listeners.forEach((l) => l());
}

if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key === KEY) {
      current = read();
      listeners.forEach((l) => l());
    }
  });
}

export function useStreamSpeed(): StreamSpeed {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
    () => "off",
  );
}
