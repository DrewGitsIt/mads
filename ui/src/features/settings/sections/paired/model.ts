import type { DeviceRow, Level, SelfDevice } from "../../../../router/devices";
import type { Envelope } from "../../../../router/types";

/**
 * Settings → Paired devices' wording and rules, kept out of the components
 * so each is tested on its own (docs/mockups/paired-devices-v3.html, and
 * docs/MESH-AUTH-DESIGN.md "Settings → Paired devices (A7) UI").
 */

export type Kind = "computer" | "phone" | "unknown";

/** A device's kind, from the platform it reports: never chosen by the user. */
export function kindOf(platform: string | undefined): Kind {
  switch ((platform ?? "").toLowerCase()) {
    case "darwin":
    case "linux":
    case "windows":
      return "computer";
    case "ios":
    case "android":
      return "phone";
    default:
      return "unknown";
  }
}

const PLATFORM_LABEL: Record<string, string> = {
  darwin: "macOS",
  linux: "Linux",
  windows: "Windows",
  ios: "iOS",
  android: "Android",
};

export function platformLabel(platform: string | undefined): string {
  return PLATFORM_LABEL[(platform ?? "").toLowerCase()] ?? "unknown platform";
}

export const KIND_LABEL: Record<Kind, string> = { computer: "computer", phone: "phone", unknown: "device of unknown platform" };

/** The level a new pairing suggests for a kind. */
export const SUGGESTED: Record<Kind, Level> = { computer: "control", phone: "chat", unknown: "view" };

/** A level as the sentence's verb: "may [verb] mads on this …". */
export const VERB: Record<Level, string> = {
  none: "not use",
  view: "view",
  chat: "chat with",
  control: "control",
  admin: "administer",
};

export const LEVEL_DESC: Record<Level, string> = {
  none: "Nothing. It can't reach mads here.",
  view: "See sessions, transcripts and live output.",
  chat: "View, plus message, interrupt and answer prompts in sessions that already exist, at their trust.",
  control: "Everything you can do: start sessions at any trust, run a shell, commit, land and push.",
  admin: "Control, plus pairing devices and changing what they may do here. Only ever set on the device itself.",
};

export const rank = (l: Level | undefined): number => ["none", "view", "chat", "control", "admin"].indexOf(l ?? "none");

/** Raising to control or administer asks first. */
export function needsConfirm(from: Level, to: Level): boolean {
  return rank(to) > rank(from) && (to === "control" || to === "admin");
}

/** What raising a device to control (or administer) lets it do, for the confirmation. */
export function raiseLine(device: string, level: Level, host: string): string {
  const base = `${device} will be able to start Full access sessions, run a shell, and commit, land and push on ${host}.`;
  return level === "admin" ? `${base} It can also pair devices and change what every device may do there.` : base;
}

/** The host's own short name for "this …": "this MacBook-Air", "this pop-os". */
export function hostWord(self: SelfDevice | undefined): string {
  const name = self?.name?.trim();
  if (!name) return "device";
  // "Drews-MacBook-Air" reads "this MacBook-Air": drop a possessive owner prefix.
  const m = /^[A-Za-z]+[’']?s-(.+)$/.exec(name);
  return m ? m[1] : name;
}

/** A row's level: no grant, and a grant at "" (none, its limits kept), both read none. */
export function levelOf(row: DeviceRow): Level {
  return row.grant?.level || "none";
}

/** "administer, capped at control by your tailnet policy", or "" when nothing caps it. */
export function cappedLine(row: DeviceRow): string {
  const r = row.remote;
  if (!r?.tailnet_capped || !r.granted) return "";
  return `${VERB[r.granted]}, capped at ${VERB[r.level ?? "none"]} by your tailnet policy`;
}

export function kindOfRow(row: DeviceRow): Kind {
  return kindOf(row.remote?.platform);
}

/** Whether the device hosts mads sessions this one could view (and connect to). */
export function hostsSessions(row: DeviceRow): boolean {
  if (row.remote?.hosts_sessions !== undefined) return row.remote.hosts_sessions;
  if (kindOfRow(row) === "phone") return false;
  return Boolean(row.mesh_url);
}

export function isViewed(row: DeviceRow): boolean {
  return Boolean(row.mesh_url) && row.viewed !== false;
}

/** Fingerprint in groups of four: the first four are its short form. */
export function fingerprintGroups(fp: string): string[] {
  return fp.match(/.{1,4}/g) ?? [];
}

export function shortFingerprint(fp: string): string {
  return fingerprintGroups(fp).slice(0, 4).join("-");
}

// --- Time ------------------------------------------------------------------------------

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "just now", "5 min ago", "3 h ago", then "Nov 18". */
export function relTime(iso: string | undefined, now: Date = new Date()): string {
  if (!iso) return "";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  const s = Math.max(0, Math.round((now.getTime() - t.getTime()) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${MONTHS[t.getMonth()]} ${t.getDate()}`;
}

/** "Nov 18, 08:12". */
export function dateTime(iso: string | undefined): string {
  if (!iso) return "";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  return `${MONTHS[t.getMonth()]} ${t.getDate()}, ${String(t.getHours()).padStart(2, "0")}:${String(t.getMinutes()).padStart(2, "0")}`;
}

// --- The row's cells ---------------------------------------------------------------------

export interface LetsCell {
  text: string;
  level: Level | "na";
  stale: boolean;
  tip: string;
}

/** "Lets this <host> …": what the device grants this one, from its hello. */
export function letsCell(row: DeviceRow, host: string, now?: Date): LetsCell {
  const dn = row.name;
  if (!hostsSessions(row)) {
    return { text: "—", level: "na", stale: false, tip: `${dn} doesn't host mads sessions, so there's nothing it could let this ${host} do.` };
  }
  if (!row.remote?.as_of) {
    return { text: "—", level: "na", stale: false, tip: "Not reached yet." };
  }
  const level: Level = row.remote.level ?? "none";
  const capped = cappedLine(row);
  if (capped) {
    return {
      text: `${VERB[level]} (capped)`,
      level,
      stale: row.reachability === "unreachable",
      tip: `${dn} lets this ${host} ${capped}. The cap is this ${host}'s tailnet capability for ${dn}, set in your tailnet's policy.`,
    };
  }
  if (row.reachability === "unreachable") {
    return {
      text: VERB[level],
      level,
      stale: true,
      tip: `Last known, as of ${relTime(row.remote.as_of, now)}. ${dn} is offline right now.`,
    };
  }
  return {
    text: VERB[level],
    level,
    stale: false,
    tip: `What ${dn} lets this ${host} do with mads there. Set on ${dn}, in its Settings → Paired devices.`,
  };
}

export type Dot = "ok" | "warn" | "bad" | "off" | "muted";

export interface StatusCell {
  text: string;
  dot: Dot;
  tip: string;
}

function usedLine(row: DeviceRow, host: string, now?: Date): string {
  return row.last_used_at ? `It last used this ${host} ${relTime(row.last_used_at, now)}.` : `It has never used this ${host}.`;
}

/**
 * The Status column, worded by direction: a device that hosts mads says
 * whether this one reaches it; one that doesn't says when it last used
 * this one.
 */
export function statusCell(row: DeviceRow, host: string, now?: Date): StatusCell {
  const dn = row.name;
  if (!hostsSessions(row) || !row.reachability) {
    const never = !row.last_used_at;
    return {
      text: never ? `Never used this ${host}` : `Last used this ${host} ${relTime(row.last_used_at, now)}`,
      dot: never ? "off" : "ok",
      tip: hostsSessions(row)
        ? `The last time ${dn} made a request to mads on this ${host}.`
        : `The last time ${dn} made a request to mads on this ${host}. ${dn} doesn't host mads, so this ${host} never connects to it.`,
    };
  }
  switch (row.reachability) {
    case "connected": {
      const ms = row.rtt_ms ? ` · ${row.rtt_ms} ms` : "";
      return {
        text: `Connected${ms}`,
        dot: "ok",
        tip: `This ${host} reaches mads on ${dn}${row.rtt_ms ? ` (${row.rtt_ms} ms round trip)` : ""}. ${usedLine(row, host, now)}`,
      };
    }
    case "unreachable":
      return {
        text: "Offline",
        dot: "bad",
        tip: `This ${host} can't reach ${dn} right now${row.remote?.as_of ? `; last reached ${relTime(row.remote.as_of, now)}` : ""}. ${usedLine(row, host, now)}`,
      };
    case "denied":
      return {
        text: `Doesn't let this ${host} use mads`,
        dot: "warn",
        tip: `${dn} answers, but lets this ${host} not use mads there. That's set on ${dn}. ${usedLine(row, host, now)}`,
      };
    case "version_mismatch":
      return { text: "Needs a mads update", dot: "warn", tip: `${dn} runs a mads this one can't talk to. Update mads on both.` };
    case "unpaired":
      return { text: "Not paired", dot: "warn", tip: `No key is pinned for ${dn} here, so this ${host} never connects to it.` };
    default:
      return { text: "Checking…", dot: "muted", tip: `This ${host} hasn't reached ${dn} yet since it started.` };
  }
}

/** Why Show sessions is off limits, or "" when it can be switched. */
export function showSessionsWhy(row: DeviceRow, host: string): string {
  if (!hostsSessions(row) || !row.mesh_url || !row.fingerprint) return `${row.name} doesn't host mads sessions.`;
  if (row.remote?.as_of && !row.remote.level && row.reachability !== "unreachable") {
    return `${row.name} lets this ${host} not use mads, so there are no sessions to show.`;
  }
  return "";
}

// --- Activity ------------------------------------------------------------------------------

export type ActivityTone = "ok" | "neutral" | "info" | "bad";

/** A run of an activity line: plain text, or a name shown bold. */
export type ActivityPart = string | { bold: string };

export interface ActivityLine {
  seq: number;
  chip: { tone: ActivityTone; label: string };
  parts: ActivityPart[];
  time: string;
  at: string;
}

interface DevicePayload {
  name?: string;
  fingerprint?: string;
  level?: string;
  previous_level?: string;
  how?: string;
  code?: string;
  route?: string;
  method?: string;
  require_tailnet?: boolean;
  by?: string;
  platform?: string;
}

/** "device:<name>/<short>" → the device's current name (by its short fingerprint), else the name it had. */
function actorDevice(actor: string, rows: DeviceRow[]): string | undefined {
  const m = /^device:(.*)\/([^/]+)$/.exec(actor);
  if (!m) return undefined;
  const row = rows.find((r) => r.fingerprint && shortFingerprint(r.fingerprint).replace(/-/g, "") === m[2].replace(/-/g, ""));
  return row?.name ?? m[1];
}

const HOW: Record<string, string> = {
  code: "with a pairing code",
  accept: "by fingerprint (mads pair --accept)",
  ceremony: "in person",
};

const REFUSAL: Record<string, string> = {
  full_trust_chat_refused: "sent a message to a Full access session. Refused: its messages to Full access sessions are refused here.",
  held_unsupported: "sent a message to a Full access session. Refused: messages from it are held, which this device can't do yet.",
  no_access: "asked for more than it may do here. Refused.",
  trust_ceiling: "asked for a session above its highest trust. Refused.",
  no_shell: "asked for a shell. Refused.",
  unpaired_device: "connected without being paired here. Refused.",
};

const lvl = (l: string | undefined): string => VERB[(l || "none") as Level] ?? l ?? "not use";

/** One activity log entry, worded for the log. */
export function activityLine(env: Envelope, rows: DeviceRow[], now?: Date): ActivityLine {
  const p = (env.payload ?? {}) as DevicePayload;
  // A settings change is written under the store's own actor, with who
  // made it in the payload.
  const byDevice = actorDevice(env.type === "device.settings_changed" ? (p.by ?? "") : env.actor, rows);
  const named = (): ActivityPart[] => {
    let name = p.name || (p.fingerprint ? `Device ${shortFingerprint(p.fingerprint)}` : "");
    if (p.fingerprint) name = rows.find((r) => r.fingerprint === p.fingerprint)?.name ?? name;
    return name ? [{ bold: name }, " "] : [];
  };
  const by: ActivityPart[] = byDevice ? [", by ", { bold: byDevice }] : [", by this device"];
  const line = (tone: ActivityTone, label: string, parts: ActivityPart[]): ActivityLine => ({
    seq: env.seq,
    chip: { tone, label },
    parts,
    time: relTime(env.timestamp, now),
    at: env.timestamp,
  });
  const actor: ActivityPart[] = byDevice ? [{ bold: byDevice }, " "] : [];
  // A payload the Data Service couldn't read comes as {invalid_payload};
  // anything not an object is no better. Say so, rather than word it from
  // fields that aren't there.
  const raw = env.payload;
  if (raw !== undefined && (typeof raw !== "object" || raw === null || Array.isArray(raw) || "invalid_payload" in raw)) {
    return line("neutral", "Unreadable", [...actor, `${humanize(env.type)} (its details can't be read)`]);
  }
  switch (env.type) {
    case "device.paired": {
      const how = HOW[p.how ?? ""];
      return line("ok", "Paired", [...named(), `paired${how ? ` ${how}` : ""}; may ${lvl(p.level)}`]);
    }
    case "device.introduced":
      return line("info", "Introduced", [...named(), ...(byDevice ? ["introduced by ", { bold: byDevice }] : ["introduced"]), `; may ${lvl(p.level)}`]);
    case "device.grant_changed":
      if ((p.level ?? "") === (p.previous_level ?? "")) return line("neutral", "Limits", [...named(), "limits changed", ...by]);
      return line("neutral", "Level", [...named(), `changed from ${lvl(p.previous_level)} to ${lvl(p.level)}`, ...by]);
    case "device.unpaired":
      return line("neutral", "Removed", [...named(), "removed", ...by]);
    case "device.refused": {
      const who = named();
      return line("bad", "Refused", [...(who.length ? who : ["A device "]), REFUSAL[p.code ?? ""] ?? `was refused (${p.code ?? "unknown"}).`]);
    }
    case "device.code_created":
      return line("neutral", "Code", ["A pairing code was made on this device"]);
    case "device.pair_requested":
      return line("info", "Request", [...named(), "asked to pair, with this device's pairing code"]);
    case "device.pair_declined":
      return line("neutral", "Declined", [...named(), "asked to pair; declined"]);
    case "device.settings_changed":
      return line("neutral", "Setting", [`Only accept devices on my tailnet turned ${p.require_tailnet === false ? "off" : "on"}`, ...by]);
    case "session.device_message":
      return line("neutral", "Message", [...actor, "sent a message to a session"]);
    default:
      return line("neutral", "Action", [...actor, humanize(env.type)]);
  }
}

/** "session.renamed" → "session renamed". */
function humanize(type: string): string {
  return type.replace(/[._]/g, " ");
}
