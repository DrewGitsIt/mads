import { describe, expect, it } from "vitest";
import type { DeviceRow, SelfDevice, SelfMesh, TailscaleView } from "../../../../router/devices";
import { activityLine, hiddenActivity, networkNameOf } from "./model";
import {
  addressWords,
  adoptionLine,
  cardState,
  choices,
  daysUntil,
  inDays,
  knockLine,
  markNetworkChanged,
  SETTLE_MS,
  settling,
  meaningLine,
  networkTitle,
  promptMode,
  refusalLine,
  rowNetworks,
  rowProblem,
  sentence,
  hiddenLine,
  effectiveReachability,
  safeSignInURL,
  tailscaleBanner,
  tailscaleHere,
  windowClosedLine,
} from "./network";
import { net, SELF, STUDIO } from "./testRouter";

const TS = net({ id: "tailscale", recommended: true });
const HOME = net({ id: "lan:192.168.1.0/24/aa", kind: "lan", title: "Wi-Fi", ssid: "Kidwell-5G", meaning: "local_only", who_can_knock: "anyone_on_network", risk: "medium" });
const CAMPUS = net({ id: "lan:10.20.0.0/16/bb", kind: "lan", title: "Wi-Fi", ssid: "eduroam", shared: true, meaning: "local_only", who_can_knock: "anyone_on_shared_network", risk: "high" });
const PUBLIC = net({ id: "public:2601::/64", kind: "public", title: "Public IPv6", meaning: "internet", who_can_knock: "anyone_on_internet", risk: "high" });

const mesh = (over: Partial<SelfMesh> = {}): SelfMesh => ({
  configured: true,
  networks: [{ ...TS, chosen: true, listening: true }, HOME],
  port: 17380,
  public: false,
  tailnet_check: true,
  pairing_started: [],
  listening: [],
  service_seen: true,
  service_answering: true,
  ...over,
});
const self = (m: Partial<SelfMesh> = {}): SelfDevice => ({ ...SELF, mesh: mesh(m) });

describe("network wording", () => {
  it("words each network by its keys: meaning, who can knock (shared in the warning colour)", () => {
    expect(meaningLine(TS)).toBe("VPN · Works from anywhere your devices have internet.");
    expect(meaningLine(HOME)).toBe("Local network · Only while both devices are on this network.");
    expect(knockLine(TS)).toEqual({ text: "Only devices on your tailnet can try to connect. Only devices you add get in.", warn: false });
    expect(knockLine(HOME).warn).toBe(false);
    expect(knockLine(CAMPUS)).toMatchObject({ warn: true, text: expect.stringContaining("a VPN such as Tailscale is safer here") });
    expect(networkTitle(HOME)).toBe("Wi-Fi “Kidwell-5G”");
    expect(networkTitle(net({ id: "lan:x", kind: "lan", title: "", wireless: false }))).toBe("Wired network");
  });

  it("offers only networks this device is on now, never a public address", () => {
    const m = mesh({ networks: [TS, HOME, PUBLIC, net({ id: "vpn:zerotier:10.147.17.0/24", title: "VPN (ZeroTier)", available: false, chosen: true })] });
    expect(choices(m).map((n) => n.id)).toEqual(["tailscale", HOME.id]);
    expect(tailscaleHere(m)?.id).toBe("tailscale");
    expect(tailscaleHere(mesh({ networks: [HOME] }))).toBeUndefined();
    expect(tailscaleHere(mesh({ networks: [{ ...TS, available: false }] }))).toBeUndefined();
  });
});

describe("the Mesh network card", () => {
  it("is Reachable when listening on its network, and says so in plain words otherwise", () => {
    expect(cardState(self(), "pop-os")).toMatchObject({ dot: "ok", text: "Reachable" });
    expect(cardState(self({ networks: [{ ...TS, chosen: true, available: false }] }), "MacBook-Air")).toMatchObject({
      dot: "bad",
      text: "Not on it now",
      problem: { title: "This MacBook-Air isn't on Tailscale right now.", description: expect.stringContaining("Turn Tailscale back on.") },
    });
    expect(cardState(self({ service_answering: false }), "pop-os")).toMatchObject({ dot: "warn", text: "Not answering" });
    expect(cardState(self({ service_seen: false, service_answering: undefined }), "pop-os").text).toBe("Not answering");
    expect(cardState(self({ tailscale: { present: true, needs_login: true, key_expiry_off: false, admin_url: "" } }), "pop-os")).toMatchObject({
      dot: "bad",
      text: "Signed out",
    });
  });

  it("reads Getting ready… for a moment after a network change, not a warning", () => {
    const quiet = cardState(self({ service_seen: false, service_answering: undefined }), "pop-os", true);
    expect(quiet).toEqual({ dot: "muted", text: "Getting ready…", tip: "mads is starting to listen on Tailscale on this pop-os." });
    expect(cardState(self({ networks: [{ ...TS, chosen: true, available: true, listening: false }] }), "pop-os", true).text).toBe("Getting ready…");
    // Not on it, or signed out, isn't a moment's wait.
    expect(cardState(self({ networks: [{ ...TS, chosen: true, available: false }] }), "pop-os", true).text).toBe("Not on it now");
    markNetworkChanged(1000);
    expect(settling(1000 + SETTLE_MS - 1)).toBe(true);
    expect(settling(1000 + SETTLE_MS)).toBe(false);
  });

  it("the Tailscale line: signed out, then expiring within 7 days, then the date; nothing with expiry off", () => {
    const now = new Date("2026-11-23T12:00:00Z");
    const ts = (o: Partial<TailscaleView>): TailscaleView => ({ present: true, needs_login: false, key_expiry_off: false, admin_url: "", ...o });
    const chosen = { ...TS, chosen: true };
    expect(tailscaleBanner(ts({ needs_login: true }), chosen, now)).toEqual({ kind: "signed_out" });
    expect(tailscaleBanner(ts({ key_expiry: "2026-11-28T10:00:00Z" }), chosen, now)).toEqual({ kind: "expiring", days: 5 });
    expect(tailscaleBanner(ts({ key_expiry: "2027-03-03T10:00:00Z" }), chosen, now)).toEqual({ kind: "expiry", on: "Mar 3" });
    expect(tailscaleBanner(ts({ key_expiry_off: true }), chosen, now)).toBeNull();
    // Only on the Tailscale network.
    expect(tailscaleBanner(ts({ needs_login: true }), { ...HOME, chosen: true }, now)).toBeNull();
    expect(daysUntil("2026-11-23T13:00:00Z", now)).toBe(1);
    expect([inDays(0), inDays(1), inDays(5)]).toEqual(["today", "tomorrow", "in 5 days"]);
  });
});

describe("the Tailscale sign-in link (S4)", () => {
  it("opens only https links on Tailscale's login server or this node's control server", () => {
    expect(safeSignInURL("https://login.tailscale.com/a/abc")).toBe("https://login.tailscale.com/a/abc");
    expect(safeSignInURL("https://headscale.example.com/register/x", "https://headscale.example.com")).toBe("https://headscale.example.com/register/x");
    for (const bad of [
      "javascript:alert(1)",
      "data:text/html,<script>alert(1)</script>",
      "http://login.tailscale.com/a/abc",
      "https://evil.example.com/a/abc",
      "https://login.tailscale.com.evil.example/a",
      "https://user:pw@login.tailscale.com/a",
      "not a url",
      "",
    ]) {
      expect(safeSignInURL(bad, "https://headscale.example.com")).toBeUndefined();
    }
    // A control server reached over http doesn't widen it.
    expect(safeSignInURL("https://headscale.example.com/x", "http://headscale.example.com")).toBeUndefined();
  });
});

describe("a device's connection", () => {
  const row = (o: Partial<DeviceRow>): DeviceRow => ({ ...STUDIO, ...o });
  it("a problem is Offline, Not reachable from this network, or a Tailscale diagnosis", () => {
    expect(rowProblem(row({ reachability: "connected" }))).toBe(false);
    expect(rowProblem(row({ reachability: "denied" }))).toBe(false);
    expect(rowProblem(row({ reachability: "unreachable" }))).toBe(true);
    expect(rowProblem(row({ reachability: "no_shared_network" }))).toBe(true);
    expect(rowProblem(row({ reachability: "connected", diagnosis: "tailscale_key_expiring" }))).toBe(true);
  });

  it("names its networks from its addresses, never the addresses", () => {
    const a = (addr: string, label?: string, kind?: "lan" | "overlay") => ({ via: "direct", addr, label, kind, source: "hello", added_at: "", state: "untried" });
    expect(rowNetworks(row({ addresses: [a("100.1.1.1:1", "Tailscale"), a("192.168.1.20:1", "Home", "lan"), a("[fd7a::1]:1", "Tailscale")] }))).toEqual([
      "Tailscale",
      "Home",
    ]);
    expect(rowNetworks(row({ addresses: [a("10.0.0.1:1", undefined, "lan")] }))).toEqual(["a local network"]);
  });

  it("words each address's state under Troubleshoot", () => {
    expect(addressWords("in_use", "pop-os", 38)).toEqual({ dot: "ok", state: "In use · 38 ms" });
    expect(addressWords("not_on_network", "MacBook-Air")).toMatchObject({ dot: "off", state: "This MacBook-Air isn't on it now", off: true });
    expect(addressWords("other_device", "x").state).toBe("A different device answered");
    expect(addressWords("not_answering", "x").dot).toBe("bad");
    expect(addressWords("refused", "x", undefined, "loopback").state).toBe("mads won't dial it: loopback");
  });
});

describe("smaller review items", () => {
  it("the shared-network warning names the Wi-Fi when known", () => {
    expect(knockLine({ ...CAMPUS }).text).toContain("anyone on Wi-Fi “eduroam” can try to connect");
    expect(knockLine({ who_can_knock: "anyone_on_shared_network" }).text).toContain("anyone on it can try to connect");
  });
  it("the hidden count reads right for one and for many", () => {
    expect(hiddenLine(1)).toBe("1 other device on your tailnet (one without mads) isn't shown.");
    expect(hiddenLine(3)).toBe("3 other devices on your tailnet (such as cameras, printers or phones without mads) aren't shown.");
  });
  it("a row still marked connected whose every tried address fails reads unreachable, everywhere", () => {
    const a = (state: string) => ({ via: "direct", addr: "100.1.1.1:1", source: "hello", added_at: "", state });
    const stale = { ...STUDIO, reachability: "connected" as const, diagnosis: "tailscale_key_expiring", addresses: [a("not_answering"), a("not_on_network")] };
    expect(effectiveReachability(stale)).toBe("unreachable");
    expect(effectiveReachability({ ...stale, addresses: [a("not_answering"), a("works")] })).toBe("connected");
    expect(effectiveReachability({ ...stale, addresses: [a("untried")] })).toBe("connected");
  });
});

describe("pairing states", () => {
  it("words a refused request plainly, keeping the Router's message for anything else", () => {
    expect(refusalLine("declined_source", "pop-os", "x", "MacBook-Air")).toContain("pop-os declined a request from this MacBook-Air earlier");
    expect(refusalLine("too_many_pairings", "pop-os", "x")).toContain("too many pairing attempts");
    expect(refusalLine("window_closed", "pop-os", "x")).toContain("setup closed");
    expect(refusalLine("check_code_required", "spark", "x")).toBe("Compare the number with spark's screen first.");
    expect(refusalLine("vouch_withheld", "spark", "x")).toContain("another device is asking too");
    expect(refusalLine("something_new", "spark", "the Router's words")).toBe("The Router's words.");
    expect(refusalLine("rate_limited", "spark", "rate_limited: slow down")).toBe("spark is getting too many pairing attempts right now. Wait a moment, then try again.");
    expect(sentence("that pairing code isn't valid here")).toBe("That pairing code isn't valid here.");
    expect(sentence("Done!")).toBe("Done!");
  });

  it("says why setup closed itself", () => {
    expect(windowClosedLine("abandoned_pairings")).toMatchObject({ title: "Setup closed itself", description: expect.stringContaining("guess check codes") });
    expect(windowClosedLine(undefined).title).toBe("Setup closed");
  });

  it("the host's prompt: the fingerprint for a code, Tailscale's word when vouched, else the number", () => {
    const req = { id: "r", fingerprint: "F", name: "mac", state: "pending" as const, created_at: "", expires_at: "" };
    expect(promptMode(req)).toBe("code");
    expect(promptMode({ ...req, mode: "discovered", vouched: true, check_code: "482913" })).toBe("vouched");
    expect(promptMode({ ...req, mode: "discovered", vouched: true, vouch_withheld: "another_request", check_code: "482913" })).toBe("number");
    expect(promptMode({ ...req, mode: "discovered", check_code: "482913" })).toBe("number");
  });

  it("joiner-first: says the network it adopted, or why not", () => {
    expect(adoptionLine({ adopted_network: { id: "tailscale", label: "Tailscale" } }, "MacBook-Air")).toBe("Your mesh network is now Tailscale.");
    expect(adoptionLine({ not_adopted: "shared", network: "Wi-Fi “eduroam”" }, "MacBook-Air")).toContain("looks shared");
    expect(adoptionLine({ not_adopted: "gateway_unknown", network: "Wi-Fi" }, "MacBook-Air")).toContain("couldn't be told apart");
    expect(adoptionLine({}, "x")).toBe("");
  });
});

describe("activity for the mesh network", () => {
  const env = (type: string, payload: object, seq = 1) => ({ seq, timestamp: "2026-11-23T12:00:00Z", type, actor: "settings", payload });
  const text = (e: ReturnType<typeof env>, prev?: ReturnType<typeof env>) =>
    activityLine(e, [], new Date("2026-11-23T12:00:10Z"), prev)
      .parts.map((p) => (typeof p === "string" ? p : p.bold))
      .join("");
  it("names the network set, and what changed against the one before", () => {
    const ts = { id: "tailscale", listen: true, tailnet_check: true };
    expect(text(env("device.settings_changed", { networks: [ts], by: "user" }))).toBe("Mesh network set to Tailscale, by this device");
    expect(text(env("device.settings_changed", { networks: [{ ...ts, tailnet_check: false }] }), env("device.settings_changed", { networks: [ts] }))).toBe(
      "Check Tailscale identity turned off, by this device",
    );
    expect(text(env("device.settings_changed", { networks: [ts], public: true }), env("device.settings_changed", { networks: [ts] }))).toBe(
      "Made reachable from the internet, by this device",
    );
    expect(text(env("device.settings_changed", { networks: [] }))).toBe("Mesh network turned off: this device listens nowhere, by this device");
    expect(networkNameOf({ id: "vpn:zerotier:10.147.17.0/24" })).toBe("VPN (Zerotier)");
    expect(networkNameOf({ id: "lan:x", ssid: "Kidwell-5G" })).toBe("Wi-Fi “Kidwell-5G”");
  });
  it("words pairings started and abandoned, a joiner's mismatch, and a level no longer capped", () => {
    expect(text(env("device.pair_started", { name: "spark", network: "tailscale" }))).toBe("spark started pairing, on Tailscale");
    expect(text(env("device.pairing_abandoned", { abandoned: 3, started: 4 }))).toBe("Setup closed itself: 3 pairings were started and never finished");
    expect(text(env("device.pair_declined", { name: "spark", joiner_mismatch: true }))).toBe(
      "spark said the number on its screen didn't match, so it was removed here",
    );
    expect(text(env("device.level_uncapped", { name: "spark", level: "admin", capability: "control", had_capability: true }))).toBe(
      "spark may now administer mads here, as granted: the tailnet policy no longer holds it to control",
    );
    expect(text(env("device.paired", { name: "spark", how: "discovered", level: "control" }))).toBe("spark paired during setup; may control here");
  });
  it("words a connected device's path changing", () => {
    expect(
      text(env("peer.reachability_changed", { peer_id: "per_pop", peer: "pop-os", from: "connected", to: "connected", via: { label: "Tailscale" }, via_from: { label: "Home" } })),
    ).toBe("pop-os now reached via Tailscale (was Home)");
  });
  it("a found device's request isn't worded as a pairing code's; a level no longer capped shows only where there was a cap", () => {
    expect(text(env("device.pair_requested", { name: "spark", mode: "discovered" }))).toBe("spark asked to pair, after finding this device during setup");
    expect(text(env("device.pair_requested", { name: "spark" }))).toBe("spark asked to pair, with this device's pairing code");
    expect(hiddenActivity(env("device.level_uncapped", { name: "spark", level: "control", had_capability: false }))).toBe(true);
    expect(hiddenActivity(env("device.level_uncapped", { name: "spark", level: "admin", capability: "control", had_capability: true }))).toBe(false);
  });
  it("hides discovery probes and the setup window's minute renewals", () => {
    expect(hiddenActivity(env("device.discovered", { kind: "probe" }))).toBe(true);
    expect(hiddenActivity(env("device.discoverable", { renew: true }))).toBe(true);
    expect(hiddenActivity(env("device.discoverable", { until: "2026-11-23T12:10:00Z" }))).toBe(false);
    expect(text(env("device.discoverable", { until: "2026-11-23T12:10:00Z" }))).toContain("Setup opened");
  });
});
