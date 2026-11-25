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

// Today's toDateString(), kept until the next local midnight so a render
// doesn't build a Date per stamp to learn what day it is.
let today: { key: string; from: number; until: number } | undefined;

// Every cached string is local time: a time-zone change (travel, a system
// setting) drops them all. Checked at most once a second.
let zone: { name: string; checkedAt: number } | undefined;
const ZONE_CHECK_MS = 1000;

function checkZone(ms: number): void {
  if (zone && ms - zone.checkedAt < ZONE_CHECK_MS && ms >= zone.checkedAt) return;
  const name = new Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (zone && zone.name !== name) {
    stamps.clear();
    today = undefined;
    timeFormat = undefined;
    dayFormat = undefined;
  }
  zone = { name, checkedAt: ms };
}

/**
 * What every stamp's label and title are made against: today, in this time
 * zone. A memoized message re-renders when it changes, so its stamp follows
 * midnight and a zone change without re-rendering on every commit.
 */
export function stampsKey(): string {
  const day = todayKey(); // first: it checks the zone
  return `${day} ${zone?.name ?? ""}`;
}

function todayKey(): string {
  const ms = Date.now();
  checkZone(ms);
  if (!today || ms >= today.until || ms < today.from) {
    const now = new Date(ms);
    const y = now.getFullYear();
    const m = now.getMonth();
    const d = now.getDate();
    today = { key: now.toDateString(), from: new Date(y, m, d).getTime(), until: new Date(y, m, d + 1).getTime() };
  }
  return today.key;
}

/** "22:59" today, "Sep 28 16:54" otherwise — short enough for a hover stamp. */
export function formatMessageTime(date: Date, now?: Date): string {
  let day: string;
  if (now) {
    checkZone(Date.now());
    day = now.toDateString();
  } else day = todayKey();
  timeFormat ??= new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
  const time = timeFormat.format(date);
  if (date.toDateString() === day) return time;
  dayFormat ??= new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
  return `${dayFormat.format(date)} ${time}`;
}

/** A message's stamp, everything a `<time>` needs: `iso` for dateTime, the
 * full local `title` shown on hover, and the short `label`. */
export interface MessageStamp {
  date: Date;
  iso: string;
  title: string;
  label: string;
}

interface StampEntry extends MessageStamp {
  day: string; // date.toDateString()
  time: string; // "22:59"
  labelFor: string; // the today key `label` was made against
}

// By timestamp string: a row's stamp never changes, and the stream renders
// every stamp on every commit. Bounded so switching through many sessions
// can't grow it without limit; dropping the oldest only costs a re-format.
const STAMP_CAP = 20_000;
const stamps = new Map<string, StampEntry | null>();

/**
 * The parsed and formatted stamp for a transcript timestamp, or undefined
 * when it has none (parseMessageTime). Formats once per timestamp; the label
 * follows the day (today → "22:59", else "Sep 28 16:54") across midnight.
 */
export function messageStamp(timestamp?: string): MessageStamp | undefined {
  if (!timestamp) return undefined;
  const key = todayKey(); // first: a zone change clears the cache
  let entry = stamps.get(timestamp);
  if (entry === undefined) {
    const date = parseMessageTime(timestamp);
    entry = date
      ? {
          date,
          iso: date.toISOString(),
          title: date.toLocaleString(),
          label: "",
          day: date.toDateString(),
          time: "",
          labelFor: "",
        }
      : null;
    if (stamps.size >= STAMP_CAP) stamps.delete(stamps.keys().next().value!);
    stamps.set(timestamp, entry);
  }
  if (!entry) return undefined;
  if (entry.labelFor !== key) {
    timeFormat ??= new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
    if (!entry.time) entry.time = timeFormat.format(entry.date);
    if (entry.day === key) {
      entry.label = entry.time;
    } else {
      dayFormat ??= new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
      entry.label = `${dayFormat.format(entry.date)} ${entry.time}`;
    }
    entry.labelFor = key;
  }
  return entry;
}
