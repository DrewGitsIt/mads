import type { DeviceRow, JoinStatus, NetworkView, PairRequest, SelfDevice, SelfMesh, TailscaleView } from "../../../../router/devices";

/**
 * The mesh network's wording and state rules (docs/MESH-NETWORK-DESIGN.md
 * U1–U4, "Fewer clicks, and Tailscale sign-in", and the text keys of "As
 * built: Part 1"; the approved docs/mockups/network-reachability-v4.html),
 * kept out of the components so each rule is tested on its own.
 */

export const TAILSCALE_ID = "tailscale";
/** Tailscale's admin console: every admin link is this constant, never a URL from elsewhere. */
export const TAILSCALE_ADMIN = "https://login.tailscale.com/admin/machines";

/**
 * Whether a sign-in link from tailscaled may be opened (S4): https only, on
 * Tailscale's login server or this node's own control server (its
 * ControlURL, https too). Never javascript:, data:, http or anywhere else.
 */
export function safeSignInURL(raw: string | undefined, controlURL?: string): string | undefined {
  if (!raw) return undefined;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return undefined;
  }
  if (u.protocol !== "https:" || u.username || u.password) return undefined;
  const allowed = new Set(["login.tailscale.com"]);
  if (controlURL) {
    try {
      const c = new URL(controlURL);
      if (c.protocol === "https:") allowed.add(c.host.toLowerCase());
    } catch {
      // not a URL: Tailscale's only
    }
  }
  return allowed.has(u.host.toLowerCase()) ? u.href : undefined;
}

/** The VPN install guides the "network in common" tips link to. */
export const VPN_GUIDES: [string, string][] = [
  ["Tailscale", "https://tailscale.com/kb/1017/install"],
  ["ZeroTier", "https://docs.zerotier.com/start"],
  ["NetBird", "https://docs.netbird.io/how-to/getting-started"],
];

/** A network's one-line meaning, by its `meaning` key. */
export function meaningLine(n: Pick<NetworkView, "meaning">): string {
  switch (n.meaning) {
    case "vpn_anywhere":
      return "VPN · Works from anywhere your devices have internet.";
    case "local_only":
      return "Local network · Only while both devices are on this network.";
    case "internet":
      return "The internet · Reachable from anywhere, if your router lets it through.";
    default:
      return "";
  }
}

/** Who can try to connect, by its `who_can_knock` key. Shared: the warning colour, naming the Wi-Fi when known. */
export function knockLine(n: Pick<NetworkView, "who_can_knock"> & Partial<Pick<NetworkView, "ssid">>): { text: string; warn: boolean } {
  switch (n.who_can_knock) {
    case "tailnet_only":
      return { text: "Only devices on your tailnet can try to connect. Only devices you add get in.", warn: false };
    case "vpn_members":
      return { text: "Only devices on this VPN can try to connect. Only devices you add get in.", warn: false };
    case "anyone_on_network":
      return { text: "Anyone on this network can try to connect. Only devices you add get in.", warn: false };
    case "anyone_on_shared_network":
      return {
        text: `A shared network: anyone on ${n.ssid ? `Wi-Fi “${n.ssid}”` : "it"} can try to connect. Only devices you add get in, but a VPN such as Tailscale is safer here.`,
        warn: true,
      };
    case "anyone_on_internet":
      return { text: "Anyone on the internet can try to connect. Only devices you add get in.", warn: true };
    default:
      return { text: "Only devices you add get in.", warn: false };
  }
}

/** What a network is called in a sentence: "Tailscale", "Wi-Fi “Kidwell-5G”", "Wired network". */
export function networkTitle(n: Pick<NetworkView, "title" | "ssid" | "wireless" | "kind">): string {
  if (n.ssid) return `Wi-Fi “${n.ssid}”`;
  return n.title || (n.kind === "lan" ? (n.wireless ? "Wi-Fi network" : "Wired network") : "Network");
}

/** The icon a network card shows. */
export function networkIcon(n: Pick<NetworkView, "kind" | "wireless">): "overlay" | "wifi" | "public" {
  if (n.kind === "overlay") return "overlay";
  if (n.kind === "public") return "public";
  return "wifi";
}

/** The networks Set up mesh offers: ones this device is on now, never a public address (U1). */
export function choices(mesh: SelfMesh | undefined): NetworkView[] {
  return (mesh?.networks ?? []).filter((n) => n.available && n.kind !== "public");
}

/** Tailscale, when this device is on it now: Set up with Tailscale, and the quick "Use Tailscale?" card. */
export function tailscaleHere(mesh: SelfMesh | undefined): NetworkView | undefined {
  return choices(mesh).find((n) => n.id === TAILSCALE_ID);
}

/** The chosen mesh network (the MVP has one). */
export function chosenNetwork(mesh: SelfMesh | undefined): NetworkView | undefined {
  return mesh?.networks.find((n) => n.chosen);
}

/** The network names a sentence uses: "Tailscale", never an address. */
export function wordFor(n: Pick<NetworkView, "id" | "title" | "ssid" | "wireless" | "kind"> | undefined): string {
  if (!n) return "your mesh network";
  if (n.id === TAILSCALE_ID) return "Tailscale";
  return networkTitle(n);
}

// --- Tailscale ---------------------------------------------------------------

const DAY = 86_400_000;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** Whole days until `iso` (0 when it's today or past). */
export function daysUntil(iso: string | undefined, now: Date = new Date()): number | undefined {
  if (!iso) return undefined;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return undefined;
  return Math.max(0, Math.ceil((t - now.getTime()) / DAY));
}

/** "Mar 3". */
export function shortDate(iso: string | undefined): string {
  if (!iso) return "";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  return `${MONTHS[t.getMonth()]} ${t.getDate()}`;
}

/** "in 5 days", "tomorrow", "today". */
export function inDays(n: number): string {
  if (n <= 0) return "today";
  if (n === 1) return "tomorrow";
  return `in ${n} days`;
}

export type TailscaleBanner =
  | { kind: "signed_out" }
  | { kind: "expiring"; days: number }
  | { kind: "expiry"; on: string }
  | null;

/**
 * The Mesh network card's Tailscale line (prevention, U2 and "Tailscale
 * sign-in"): signed out first, then a key expiring within 7 days, then the
 * date it will (for a machine reached remotely); nothing with expiry off.
 */
export function tailscaleBanner(ts: TailscaleView | undefined, chosen: NetworkView | undefined, now: Date = new Date()): TailscaleBanner {
  if (!ts?.present || chosen?.id !== TAILSCALE_ID) return null;
  if (ts.needs_login) return { kind: "signed_out" };
  if (ts.key_expiry_off || !ts.key_expiry) return null;
  const days = daysUntil(ts.key_expiry, now) ?? 0;
  if (days <= 7) return { kind: "expiring", days };
  return { kind: "expiry", on: shortDate(ts.key_expiry) };
}

// --- The Mesh network card -------------------------------------------------------

/** How long after choosing a network the card reads "Getting ready…" rather than a warning: the Mesh Service starts listening within a few seconds. */
export const SETTLE_MS = 20_000;
let networkChangedAt = 0;
/** A network was just chosen (Set up mesh, Change…, or adopted by pairing). */
export function markNetworkChanged(at = Date.now()): void {
  networkChangedAt = at;
}
/** Whether the mesh network changed within SETTLE_MS. */
export function settling(now = Date.now()): boolean {
  return now - networkChangedAt < SETTLE_MS;
}

export type CardState = { dot: "ok" | "warn" | "bad" | "muted"; text: string; tip: string; problem?: { title: string; description: string } };

/**
 * The card's state: Reachable, Not on it now, Signed out, or the Mesh
 * Service not answering / not listening, which read "Getting ready…" for a
 * moment after a network change (`justChanged`).
 */
export function cardState(self: SelfDevice | undefined, host: string, justChanged = false): CardState {
  const mesh = self?.mesh;
  const n = chosenNetwork(mesh);
  if (!mesh || !n) return { dot: "bad", text: "Not set up", tip: "" };
  const word = wordFor(n);
  if (n.id === TAILSCALE_ID && mesh.tailscale?.needs_login) {
    return { dot: "bad", text: "Signed out", tip: `Tailscale is signed out on this ${host}, so your devices can't reach it.` };
  }
  if (!n.available) {
    const fix = n.id === TAILSCALE_ID ? "Turn Tailscale back on." : "Go back to that network, or choose a VPN such as Tailscale under Change…";
    return {
      dot: "bad",
      text: "Not on it now",
      tip: `This ${host} isn't on ${word} right now.`,
      problem: {
        title: `This ${host} isn't on ${word} right now.`,
        description: `Your devices can't reach it, and it can only reach devices on networks it's on. ${fix}`,
      },
    };
  }
  if (justChanged && (!mesh.service_seen || mesh.service_answering === false || !n.listening)) {
    return { dot: "muted", text: "Getting ready…", tip: `mads is starting to listen on ${word} on this ${host}.` };
  }
  if (!mesh.service_seen || mesh.service_answering === false) {
    return {
      dot: "warn",
      text: "Not answering",
      tip: `mads's Mesh Service isn't answering on this ${host}.`,
      problem: {
        title: `mads's Mesh Service isn't answering on this ${host}.`,
        description: "Your devices can't reach mads here until it runs. Restart mads, or check that its Mesh Service is running.",
      },
    };
  }
  if (!n.listening) {
    return {
      dot: "warn",
      text: "Not listening",
      tip: n.error ? `Not listening on ${word}: ${n.error}.` : `Not listening on ${word} yet.`,
      problem: { title: `mads isn't listening on ${word} yet.`, description: n.error ? `${capitalize(n.error)}.` : "It starts within a few seconds." },
    };
  }
  return { dot: "ok", text: "Reachable", tip: `Your other devices can reach mads on this ${host} over ${word}.` };
}

function capitalize(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

// --- A device's row ------------------------------------------------------------------

/**
 * A row's reachability from the freshest of what the Router says: its
 * addresses' states are re-read on every dial (Try again now), so a row
 * still marked connected whose every tried address now fails reads as
 * unreachable, and the Status and Troubleshoot never disagree.
 */
export function effectiveReachability(row: DeviceRow): DeviceRow["reachability"] {
  if (row.reachability !== "connected" || !row.addresses?.length) return row.reachability;
  const tried = row.addresses.filter((a) => a.state !== "not_on_network" && a.state !== "untried" && a.state !== "refused");
  if (tried.length > 0 && tried.every((a) => a.state === "not_answering" || a.state === "other_device")) return "unreachable";
  return row.reachability;
}

/** Whether a row has a connection problem: Troubleshoot opens by itself (and the row with it). */
export function rowProblem(row: DeviceRow): boolean {
  const r = effectiveReachability(row);
  return r === "unreachable" || r === "no_shared_network" || Boolean(row.diagnosis);
}

/** A device's networks, by name, from its addresses: "Tailscale and Wired home network". */
export function rowNetworks(row: DeviceRow): string[] {
  const out: string[] = [];
  for (const a of row.addresses ?? []) {
    const l = a.label || (a.kind === "overlay" ? "a VPN" : a.kind === "lan" ? "a local network" : a.kind === "public" ? "the internet" : "");
    if (l && !out.includes(l)) out.push(l);
  }
  return out;
}

export const joinAnd = (xs: string[]): string => (xs.length <= 1 ? (xs[0] ?? "") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

export type AddressWords = { dot: "ok" | "warn" | "bad" | "off"; state: string; off?: boolean };

/** One address's state under Troubleshoot connection. */
export function addressWords(state: string, host: string, rtt?: number, error?: string): AddressWords {
  switch (state) {
    case "in_use":
      return { dot: "ok", state: `In use${rtt ? ` · ${rtt} ms` : ""}` };
    case "works":
      return { dot: "ok", state: "Works (backup)" };
    case "not_on_network":
      return { dot: "off", state: `This ${host} isn't on it now`, off: true };
    case "not_answering":
      return { dot: "bad", state: "Not answering" };
    case "other_device":
      return { dot: "bad", state: "A different device answered" };
    case "refused":
      return { dot: "off", state: `mads won't dial it${error ? `: ${error}` : ""}`, off: true };
    default:
      return { dot: "off", state: "Not tried yet", off: true };
  }
}

/** Where an address came from. */
export function sourceWords(source: string, device: string): string {
  switch (source) {
    case "code":
      return "From a pairing code";
    case "hello":
      return `Reported by ${device}`;
    case "user":
      return "Added by you";
    case "joiner":
      return `Given by ${device} when it paired`;
    case "introduced":
      return "From an introduction";
    case "discovered":
      return "Found during setup";
    default:
      return "Saved address";
  }
}

// --- Pairing ---------------------------------------------------------------------------

/** Why the setup window closed early, worded (window_closed_reason / a renewal's reason). */
export function windowClosedLine(reason: string | undefined): { title: string; description: string } {
  if (reason === "abandoned_pairings") {
    return {
      title: "Setup closed itself",
      description:
        "Several devices started pairing and didn't finish. Someone may be trying to guess check codes. Open it again to go on, or use a pairing code.",
    };
  }
  return {
    title: "Setup closed",
    description: "This device stopped being findable (setup stays open at most 30 minutes). Open it again to go on.",
  };
}

/**
 * A refused discovered request (POST /devices/discovered/{fp}/request) or
 * approval, in plain words. Unknown codes keep the Router's own message.
 */
export function refusalLine(code: string | undefined, device: string, message: string, host = "device"): string {
  switch (code) {
    case "declined_source":
      return `${device} declined a request from this ${host} earlier, so it takes no more from here while its setup is open. Ask its user, or use a pairing code.`;
    case "too_many_pairings":
      return `${device} has had too many pairing attempts while its setup was open. Close Add devices on ${device}, open it again, and try once more, or use a pairing code.`;
    case "too_many_requests":
      return `${device} is answering other devices right now. Try again in a moment.`;
    case "not_discoverable":
      return `${device} isn't adding devices now. Open Add devices on ${device}, then try again.`;
    case "window_closed":
      return "This device's setup closed. Open Add devices again to go on.";
    case "already_paired":
      return `${device} is already paired with this ${host}.`;
    case "no_shared_network":
      return `${device} isn't on a network this ${host} is on right now.`;
    case "host_unreachable":
      return `Couldn't reach ${device}. Is it awake, with mads running and its firewall letting mads in?`;
    case "rate_limited":
      return `${device} is getting too many pairing attempts right now. Wait a moment, then try again.`;
    case "devices_unavailable":
    case "pairing_unavailable":
      return `${device}'s mads isn't ready. Try again in a moment.`;
    case "host_key_mismatch":
      return `The device answering isn't the ${device} that was found. Nothing was paired.`;
    case "check_code_required":
      return `Compare the number with ${device}'s screen first.`;
    case "vouch_withheld":
      return `Tailscale's confirmation no longer holds: another device is asking too. Compare the number with ${device}'s screen first.`;
    default:
      return sentence(message);
  }
}

/** Retryable join failures refusalLine words itself; any other retryable one is a connection that failed. */
const RETRY_WORDED = new Set(["host_unreachable", "rate_limited", "devices_unavailable", "pairing_unavailable", "too_many_requests"]);

/** A failed join or discovered request (F6 R7): couldn't connect reads as that, the rest as refusalLine says. */
export function joinFailLine(e: { code?: string; message: string; retryable?: boolean }, device: string, host = "device"): string {
  if (e.retryable && !RETRY_WORDED.has(e.code ?? "")) return `Couldn't reach ${device}.`;
  return refusalLine(e.code, device, e.message, host);
}

/**
 * Add devices' found list couldn't be read (Q14): in words, never "GET …
 * 502". A Router answer with a code says its own reason; anything else is
 * this device's mads not answering, with the likeliest fix for Tailscale
 * when its identity check is on (another tailnet user is turned away).
 */
export function discoveryErrorLine(err: { message: string; code?: string; status?: number }, host: string, mesh: SelfMesh | undefined): string {
  if (err.code && err.code !== "timeout") return `Couldn't look for devices: ${sentence(err.message)}`;
  const n = chosenNetwork(mesh);
  const base = `This ${host} couldn't look for devices on ${wordFor(n)} just now. It keeps trying.`;
  if (n?.id === TAILSCALE_ID && mesh?.tailnet_check) {
    return `${base} If your devices are signed in to Tailscale as different users, turn off Check Tailscale identity (Mesh network → Advanced options) on each of them.`;
  }
  return base;
}

/** A message from elsewhere as a sentence: a capital, and a full stop. */
export function sentence(msg: string): string {
  const t = msg.trim();
  if (!t) return t;
  const s = t[0].toUpperCase() + t.slice(1);
  return /[.!?…]$/.test(s) ? s : `${s}.`;
}

/** "3 other devices on your tailnet (…) aren't shown", for any count. */
export function hiddenLine(n: number): string {
  if (n === 1) return "1 other device on your tailnet (one without mads) isn't shown.";
  return `${n} other devices on your tailnet (such as cameras, printers or phones without mads) aren't shown.`;
}

/** Why Tailscale's vouch is withheld on the host's prompt: show the number anyway. */
export function withheldLine(reason: string | undefined, joiner: string): string {
  if (reason === "name_collides") {
    return `Tailscale confirms ${joiner} is one of your devices, but you already have a device with that name. Compare the number.`;
  }
  return `Tailscale confirms ${joiner} is one of your devices, but another device is asking at the same time. Compare the number to tell which is which.`;
}

/** The host's prompt: is this a discovered request, and what does it show? */
export function promptMode(req: PairRequest): "code" | "vouched" | "number" {
  if (req.mode !== "discovered") return "code";
  if (req.vouched && !req.vouch_withheld) return "vouched";
  return "number";
}

/** The joiner's word on a join that adopted a network, or didn't. */
export function adoptionLine(st: Pick<JoinStatus, "adopted_network" | "not_adopted" | "network">, host: string): string {
  if (st.adopted_network) return `Your mesh network is now ${st.adopted_network.label}.`;
  if (st.not_adopted === "shared") {
    return `${st.network || "That network"} looks shared (a campus or hotel network), so it isn't this ${host}'s mesh network. Choose it in Set up mesh if you mean to, or use a VPN such as Tailscale.`;
  }
  if (st.not_adopted === "gateway_unknown") {
    return `${st.network || "That network"} couldn't be told apart from look-alike networks yet, so it isn't this ${host}'s mesh network. Set up mesh in a moment to choose it.`;
  }
  return "";
}
