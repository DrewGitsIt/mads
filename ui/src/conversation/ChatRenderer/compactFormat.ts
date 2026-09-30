import type { CompactMarker } from "../../router/types";

/** 258267 → "258k", 950 → "950". */
export function formatTokens(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n);
}

/** 131618 → "2m 12s", 45000 → "45s". */
export function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  return s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`;
}

/** "Conversation compacted" / "Conversation auto-compacted". */
export function compactTitle(marker: CompactMarker): string {
  return marker.trigger === "auto" ? "Conversation auto-compacted" : "Conversation compacted";
}

/** The marker's detail clauses: who asked, the token drop, how long it took. */
export function compactDetails(marker: CompactMarker): string[] {
  const parts: string[] = [];
  if (marker.trigger === "manual") parts.push("you asked");
  else if (marker.trigger === "auto") parts.push("context was full");
  if (marker.pre_tokens && marker.post_tokens) {
    parts.push(`${formatTokens(marker.pre_tokens)} → ${formatTokens(marker.post_tokens)} tokens`);
  }
  if (marker.duration_ms) parts.push(formatDuration(marker.duration_ms));
  return parts;
}

/** One plain line for surfaces that list turns as text (the pipeline feed). */
export function compactLine(marker: CompactMarker): string {
  return [compactTitle(marker), ...compactDetails(marker)].join(" · ");
}
