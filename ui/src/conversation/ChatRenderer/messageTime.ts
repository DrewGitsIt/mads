/**
 * Parses a transcript timestamp, treating Go's zero time
 * ("0001-01-01T00:00:00Z", what a live-buffer entry with no stamp encodes as)
 * and anything unparseable as unknown.
 */
export function parseMessageTime(timestamp?: string): Date | undefined {
  if (!timestamp) return undefined;
  const ms = Date.parse(timestamp);
  if (Number.isNaN(ms) || new Date(ms).getFullYear() < 2000) return undefined;
  return new Date(ms);
}

// Made once: toLocale*String with options builds a formatter per call, and
// the stream formats every row's stamp on every render.
let timeFormat: Intl.DateTimeFormat | undefined;
let dayFormat: Intl.DateTimeFormat | undefined;

/** "22:59" today, "Sep 28 16:54" otherwise — short enough for a hover stamp. */
export function formatMessageTime(date: Date, now: Date = new Date()): string {
  timeFormat ??= new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
  const time = timeFormat.format(date);
  if (date.toDateString() === now.toDateString()) return time;
  dayFormat ??= new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
  return `${dayFormat.format(date)} ${time}`;
}
