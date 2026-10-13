import type { TokenCounts } from "../../../router/types";

/** Every prompt-side token a tally counts: fresh input plus both cache halves. */
export function promptTokens(t: TokenCounts): number {
  return t.input + (t.cache_read ?? 0) + (t.cache_write ?? 0);
}

/** A meter's tone: green below 60%, amber below 80%, red from there (the mockup's mcls). */
export function meterTone(pct: number): "ok" | "warn" | "bad" {
  return pct >= 80 ? "bad" : pct >= 60 ? "warn" : "ok";
}

/** 0-100, rounded and clamped. */
export function percent(part: number, whole: number): number {
  if (whole <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((part / whole) * 100)));
}

function isSameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

/** When a plan window resets: "2:10 PM" today, "Thu 9:00 AM" otherwise. Empty when unknown. */
export function formatResetTime(iso: string | undefined, now: number): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return "";
  const d = new Date(t);
  const time = d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  if (isSameDay(d, new Date(now))) return time;
  return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${time}`;
}

/** A report's cost basis for display: "list_price" → "list price". */
export function formatBasis(basis: string): string {
  return basis.replace(/_/g, " ");
}

/** "$0.91"; sub-cent spend keeps enough digits to not read as zero. */
export function formatUsd(usd: number): string {
  return usd > 0 && usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(2)}`;
}
