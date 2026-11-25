import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { resetDevicesForTests, useSelfDevice, type DeviceRow, type DiscoveredList, type JoinStatus, type SelfDevice, type SelfMesh } from "../../../../router/devices";
import { resetMeshForTests } from "../../../../router/mesh";
import { PairedDevicesSection } from "../PairedDevicesSection";
import { MeshNetworkCard, REACHABLE_POLL_MS, STATE_POLL_MS } from "./MeshNetworkCard";
import { PasteCode } from "./pairing/PasteCode";
import { AUTO_CLOSE_MS, MeshSetup } from "./setup/MeshSetup";
import { PAIR_ARM_MS } from "./pairing/PairRequestPrompt";
import { clearPairOutcome, holdPairPromptOpen, PairRequestWatcher } from "./pairing/PairRequestWatcher";
import { connectedSince, TroubleshootConnection } from "./TroubleshootConnection";
import { DISCOVERED_POLL_MS, RENEW_MS, resetSetupWindowForTests, useSetupWindow } from "./setup/useSetup";
import { fp, net, SELF, STUDIO, stubRouter, type Call } from "./testRouter";

/** The element, once it's there. */
const q = (sel: string) =>
  waitFor(() => {
    const el = document.querySelector(sel);
    if (!el) throw new Error(`no ${sel}`);
    return el as HTMLElement;
  });

afterEach(() => {
  // Unmount first: the stores and the fake bus go after.
  cleanup();
  resetSetupWindowForTests();
  resetDevicesForTests();
  resetMeshForTests();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const TS = net({ id: "tailscale", recommended: true });
const HOME = net({ id: "lan:192.168.1.0/24/aa", kind: "lan", title: "Wi-Fi", ssid: "Kidwell-5G", meaning: "local_only", who_can_knock: "anyone_on_network", risk: "medium" });
const CAMPUS = net({ id: "lan:10.20.0.0/16/bb", kind: "lan", title: "Wi-Fi", ssid: "eduroam", shared: true, meaning: "local_only", who_can_knock: "anyone_on_shared_network" });

const meshOf = (over: Partial<SelfMesh> = {}): SelfMesh => ({
  configured: false,
  networks: [TS, HOME],
  port: 17380,
  public: false,
  tailnet_check: true,
  pairing_started: [],
  listening: [],
  service_seen: true,
  service_answering: true,
  ...over,
});
const fresh = (over: Partial<SelfMesh> = {}): SelfDevice => ({ ...SELF, mesh: meshOf(over) });
const meshed = (over: Partial<SelfMesh> = {}): SelfDevice =>
  fresh({ configured: true, networks: [{ ...TS, chosen: true, listening: true }, HOME], ...over });

const SPARK = fp("K");
const found = (over: Partial<DiscoveredList["devices"][number]> = {}): DiscoveredList => ({
  searching: true,
  started_at: new Date().toISOString(),
  networks: [{ id: "tailscale", label: "Tailscale", kind: "overlay" }],
  devices: [
    { name: "spark", platform: "linux", fingerprint: SPARK, short: "KKKK-KKKK-KKKK-KKKK", network: "tailscale", network_label: "Tailscale", via: "tailscale", paired: false, compatible: true, seen_at: "", ...over },
  ],
  hidden_count: 3,
  hidden_known: true,
});
const joinOf = (over: Partial<JoinStatus> = {}): JoinStatus => ({
  id: "KKKK:pr_1",
  state: "pending",
  host: { name: "spark", fingerprint: SPARK },
  expires_at: new Date(Date.now() + 300_000).toISOString(),
  mode: "discovered",
  check_code: "482913",
  ...over,
});

function mountSection(self: SelfDevice, rows: DeviceRow[] = [], extra: Record<string, unknown> = {}): Call[] {
  let current = self;
  const calls = stubRouter({
    "GET /devices": rows,
    "GET /devices/self": () => current,
    "GET /devices/activity": [],
    "GET /devices/pair-requests": [],
    "GET /peers": [],
    "PUT /devices/settings": (c: Call) => {
      const b = c.body as { networks?: string[] };
      current = { ...self, mesh: { ...self.mesh!, configured: true, networks: self.mesh!.networks.map((n) => ({ ...n, chosen: b.networks?.includes(n.id) ?? n.chosen })) } };
      return current;
    },
    "POST /devices/discoverable": { on: true, until: new Date(Date.now() + 600_000).toISOString() },
    "GET /devices/discovered": found(),
    ...extra,
  });
  render(<PairedDevicesSection />);
  return calls;
}

describe("the empty state", () => {
  it("with Tailscale here: Set up with Tailscale sets it in one click and opens Add devices", async () => {
    const calls = mountSection(fresh());
    await screen.findByRole("heading", { name: "Use mads across your devices" });
    expect(screen.getByRole("button", { name: "Other networks…" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "I have a pairing code" })).toBeTruthy();
    // Nothing set up: no Add devices, no device table, no Mesh network card.
    expect(screen.queryByRole("button", { name: "Add devices" })).toBeNull();
    expect(screen.queryByText("No devices yet")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Set up with Tailscale" }));
    const dialog = await screen.findByRole("dialog", { name: "Add your devices" });
    expect(calls.find((c) => c.method === "PUT" && c.path === "/devices/settings")?.body).toEqual({ networks: ["tailscale"] });
    // Not listening yet: no ✓ until the card says Reachable (Q3).
    expect(dialog.querySelector("[data-setup-state]")!.textContent).toBe("Getting this pop-os ready on Tailscale…");
    expect(dialog.querySelector("[data-setup-done]")).toBeNull();
    // The window opens; found devices are read.
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path === "/devices/discoverable" && (c.body as { on: boolean }).on)).toBe(true));
    await within(dialog).findByText("spark");
    expect(dialog.textContent).toContain("3 other devices on your tailnet");
  });

  it("the dialog and the card agree: Getting ready until the Mesh Service listens, then ✓ and Reachable within a second (Q3)", async () => {
    let set = false;
    let listening = false;
    const now = () => meshed({ networks: [{ ...TS, chosen: true, listening }, HOME] });
    mountSection(fresh(), [], {
      "GET /devices/self": () => (set ? now() : fresh()),
      "PUT /devices/settings": () => ((set = true), now()),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Set up with Tailscale" }));
    const dialog = await screen.findByRole("dialog", { name: "Add your devices" });
    expect(dialog.querySelector("[data-setup-state]")!.textContent).toBe("Getting this pop-os ready on Tailscale…");
    expect((await q("[data-mesh-state]")).textContent).toBe("Getting ready…");
    listening = true;
    await waitFor(() => expect(dialog.querySelector("[data-setup-done]")?.textContent).toBe("✓This pop-os is reachable on Tailscale. Only devices you add get in."), { timeout: 2500 });
    expect(document.querySelector("[data-mesh-state]")!.textContent).toBe("Reachable");
  });

  it("with no VPN: Set up mesh, the full list, and None of these gives the network-in-common tips", async () => {
    mountSection(fresh({ networks: [CAMPUS] }));
    fireEvent.click(await screen.findByRole("button", { name: "Set up mesh" }));
    const dialog = screen.getByRole("dialog", { name: "How will your devices reach each other?" });
    const card = within(dialog).getByRole("radio", { name: /eduroam/ });
    expect(card.textContent).toContain("Shared network");
    expect(card.querySelector("[data-warn]")!.textContent).toContain("a VPN such as Tailscale is safer here");
    // One network, never several: radios, no checkboxes.
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    fireEvent.click(within(dialog).getByText("None of these"));
    const tips = screen.getByRole("dialog", { name: "Give your devices a network in common" });
    expect(tips.textContent).toContain("It doesn't make one.");
    const links = within(tips).getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(links).toEqual(["https://tailscale.com/kb/1017/install", "https://docs.zerotier.com/start", "https://docs.netbird.io/how-to/getting-started"]);
    fireEvent.click(within(tips).getByRole("button", { name: "Check again" }));
    expect(screen.getByRole("dialog", { name: "How will your devices reach each other?" })).toBeTruthy();
  });

  it("I have a pairing code opens the paste dialog, joiner-first", async () => {
    mountSection(fresh());
    fireEvent.click(await screen.findByRole("button", { name: "I have a pairing code" }));
    expect(screen.getByRole("dialog", { name: "Paste a pairing code" })).toBeTruthy();
  });
});

describe("Set up mesh", () => {
  it("the quick card: Use Tailscale?, with the key-expiry note, and Other options", async () => {
    stubRouter({ "PUT /devices/settings": meshed() });
    const self = fresh({ tailscale: { present: true, needs_login: false, key_expiry: "2027-03-03T10:00:00Z", key_expiry_off: false, admin_url: "https://login.tailscale.com/admin/machines" } });
    render(<MeshSetup self={self} rows={[]} host="pop-os" step="quick" onClose={() => {}} onUseCode={() => {}} />);
    const d = screen.getByRole("dialog", { name: "Use Tailscale?" });
    expect(d.textContent).toContain("Tailscale will sign this pop-os out on Mar 3");
    expect(within(d).getByRole("list", { name: "Step 1 of 2" })).toBeTruthy();
    fireEvent.click(within(d).getByRole("button", { name: "Other options" }));
    const list = screen.getByRole("radiogroup", { name: "Networks this pop-os is on" });
    expect(within(list).getAllByRole("radio").map((r) => r.getAttribute("data-network"))).toEqual(["tailscale", HOME.id]);
    expect(within(list).getByRole("radio", { name: /Tailscale/ }).getAttribute("aria-checked")).toBe("true");
  });

  it("Change… saves in place, and a LAN without its router's address says why", async () => {
    stubRouter({
      "PUT /devices/settings": () => new Response(JSON.stringify({ code: "gateway_unknown", error: "x" }), { status: 422 }),
    });
    const onChanged = vi.fn();
    render(<MeshSetup self={meshed()} rows={[]} host="pop-os" step="choose" change onClose={() => {}} onUseCode={() => {}} onChanged={onChanged} />);
    const d = screen.getByRole("dialog", { name: "Mesh network" });
    expect(within(d).queryByRole("list", { name: /Step/ })).toBeNull();
    fireEvent.click(within(d).getByRole("radio", { name: /Kidwell-5G/ }));
    fireEvent.click(within(d).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(within(d).getByRole("alert").textContent).toContain("can't tell this network apart"));
    expect(onChanged).not.toHaveBeenCalled();
  });
});

describe("Add devices", () => {
  const mountAdd = (routes: Record<string, unknown>) => {
    const calls = stubRouter({
      "POST /devices/discoverable": { on: true },
      "GET /devices/discovered": found(),
      "GET /devices": [],
      ...routes,
    });
    render(<MeshSetup self={meshed()} rows={[]} host="MacBook-Air" step="add" onClose={() => {}} onUseCode={() => {}} />);
    return calls;
  };

  it("Tailscale vouched: no number, then what it granted and this device's own answer", async () => {
    let st = joinOf({ vouched: true, check_code: undefined });
    const calls = mountAdd({
      [`POST /devices/discovered/${SPARK}/request`]: () => st,
      "GET /devices/pair/KKKK%3Apr_1": () => st,
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add spark" }));
    const item = await q('[data-found="spark"]');
    await waitFor(() => expect(item.textContent).toContain("Tailscale confirms spark is one of your devices, so there's no number to compare."));
    expect(item.querySelector("[data-check-code]")).toBeNull();
    st = { ...st, state: "approved", level: "control", confirm: "matched", peer_id: "per_spark" };
    await waitFor(() => expect(item.textContent).toContain("spark lets this MacBook-Air control mads there."), { timeout: 4000 });
    expect(within(item).getByText("Added")).toBeTruthy();
    expect(calls.some((c) => c.path.endsWith("/confirm"))).toBe(false);
  });

  it("not vouched: the check code on both screens, then Did it show this number?", async () => {
    let st = joinOf();
    const calls = mountAdd({
      [`POST /devices/discovered/${SPARK}/request`]: () => st,
      "GET /devices/pair/KKKK%3Apr_1": () => st,
      "POST /devices/pair/KKKK%3Apr_1/confirm": (c: Call) => (st = { ...st, confirm: (c.body as { match: boolean }).match ? "matched" : "mismatch" }),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add spark" }));
    const item = await q('[data-found="spark"]');
    await waitFor(() => expect(item.querySelector('[data-check-code="482913"]')!.textContent).toContain("Check that spark shows the same number"));
    st = { ...st, state: "approved", level: "view", confirm: "needed" };
    await waitFor(() => expect(item.textContent).toContain("spark approved. Did it show this number?"), { timeout: 4000 });
    // "Yes" waits a moment where Add was, with "No" focused (S1).
    const yes = within(item).getByRole("button", { name: "Yes, the same number" }) as HTMLButtonElement;
    expect(yes.disabled).toBe(true);
    await waitFor(() => expect(document.activeElement?.textContent).toBe("No, it was different"));
    await waitFor(() => expect(yes.disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(within(item).getByRole("button", { name: "No, it was different" }));
    await waitFor(() => expect(item.textContent).toContain("Nothing was added here, and spark was told to undo its side."));
    expect(calls.find((c) => c.path.endsWith("/confirm"))?.body).toEqual({ match: false });
  });

  it("a refused request says why, in plain words", async () => {
    mountAdd({
      [`POST /devices/discovered/${SPARK}/request`]: () =>
        new Response(JSON.stringify({ code: "too_many_pairings", error: "spark: too many" }), { status: 429 }),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add spark" }));
    const alert = await screen.findByRole("alert");
    expect(alert.getAttribute("data-refusal")).toBe("too_many_pairings");
    expect(alert.textContent).toContain("spark has had too many pairing attempts");
  });

  it("a window that closed itself says why, and opens again", async () => {
    let n = 0;
    const calls = mountAdd({
      "POST /devices/discoverable": () =>
        ++n === 1 ? new Response(JSON.stringify({ code: "window_closed", error: "x", reason: "abandoned_pairings" }), { status: 409 }) : { on: true },
      "GET /devices/discovered": { ...found(), devices: [] },
    });
    const closed = await q('[data-window-closed="abandoned_pairings"]');
    expect(closed.textContent).toContain("Setup closed itself");
    fireEvent.click(within(closed).getByRole("button", { name: "Open again" }));
    await waitFor(() => expect(document.querySelector("[data-window-closed]")).toBeNull());
    expect(calls.filter((c) => c.path === "/devices/discoverable" && (c.body as { on: boolean }).on).length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("No mads devices found yet.")).toBeTruthy();
  });

  it("surfaces a device that started pairing", async () => {
    stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/discovered": { ...found(), devices: [] } });
    const self = meshed({
      discoverable_until: new Date(Date.now() + 600_000).toISOString(),
      pairing_started: [{ name: "thinkpad", fingerprint: fp("T"), short: "TTTT-TTTT-TTTT-TTTT", at: new Date().toISOString() }],
    });
    const r = render(<MeshSetup self={self} rows={[]} host="pop-os" step="add" onClose={() => {}} onUseCode={() => {}} />);
    expect((await screen.findByRole("list", { name: "Devices that started pairing" })).textContent).toContain(
      "thinkpad (TTTT-TTTT-TTTT-TTTT) started pairing with this pop-os just now",
    );
    // The window closed: the list goes.
    r.rerender(<MeshSetup self={{ ...self, mesh: { ...self.mesh!, discoverable_until: undefined } }} rows={[]} host="pop-os" step="add" onClose={() => {}} onUseCode={() => {}} />);
    expect(screen.queryByRole("list", { name: "Devices that started pairing" })).toBeNull();
  });
});

describe("Add devices holds still (P4)", () => {
  afterEach(() => clearPairOutcome());

  it("opened at Add devices: no steps bar; the help folds and the spinner stops once a device is found", async () => {
    let list = { ...found(), devices: [] as DiscoveredList["devices"] };
    stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/discovered": () => list, "GET /devices": [] });
    render(<MeshSetup self={meshed()} rows={[]} host="pop-os" step="add" onClose={() => {}} onUseCode={() => {}} />);
    const dialog = await screen.findByRole("dialog", { name: "Add your devices" });
    expect(dialog.querySelector("ol")).toBeNull();
    await waitFor(() => expect(dialog.textContent).toContain("Looking for mads devices on Tailscale…"));
    expect(dialog.querySelector("[role=status] [aria-hidden]")).toBeTruthy();
    expect(dialog.querySelector("details")).toBeNull();
    expect(within(dialog).getByRole("button", { name: "Use a pairing code instead" })).toBeTruthy();
    list = found();
    await within(dialog).findByText("spark", {}, { timeout: 4000 });
    const head = within(dialog).getAllByRole("status").find((e) => e.textContent?.startsWith("Found on"))!;
    expect(head.textContent).toBe("Found on Tailscale. Still looking for more.");
    expect(head.querySelector("[aria-hidden]")).toBeNull();
    const fold = dialog.querySelector("details")!;
    expect(fold.open).toBe(false);
    expect(fold.querySelector("summary")!.textContent).toBe("Don't see a device?");
    expect(fold.textContent).toContain("Use a pairing code instead");
  });

  it("a found list that can't be read says so in words, with the identity check's fix (Q14)", async () => {
    stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/discovered": () => new Response("", { status: 502 }), "GET /devices": [] });
    render(<MeshSetup self={meshed()} rows={[]} host="pop-os" step="add" onClose={() => {}} onUseCode={() => {}} />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe(
      "This pop-os couldn't look for devices on Tailscale just now. It keeps trying. If your devices are signed in to Tailscale as different users, turn off Check Tailscale identity (Mesh network → Advanced options) on each of them.",
    );
    expect(alert.textContent).not.toContain("502");
  });

  const macbook = {
    id: "pr_9",
    fingerprint: fp("Q"),
    name: "macbook",
    platform: "darwin",
    state: "pending" as const,
    mode: "discovered" as const,
    vouched: true,
    network_label: "Tailscale",
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + 300_000).toISOString(),
  };
  const mountHost = () => {
    let reqs = [macbook];
    stubRouter({
      "POST /devices/discoverable": { on: true },
      "GET /devices/discovered": { ...found(), devices: [] },
      "GET /devices": [],
      "GET /devices/self": meshed(),
      "GET /devices/pair-requests": () => reqs,
      "POST /devices/pair-requests/pr_9/approve": () => {
        reqs = [{ ...macbook, state: "approved" as never }];
        return reqs[0];
      },
    });
    const onClose = vi.fn();
    const release = holdPairPromptOpen();
    render(
      <>
        <MeshSetup self={meshed()} rows={[]} host="pop-os" step="add" onClose={onClose} onUseCode={() => {}} />
        <PairRequestWatcher />
      </>,
    );
    return { onClose, release };
  };
  const approve = async () => {
    const add = await screen.findByRole("button", { name: "Add with control" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PAIR_ARM_MS);
    });
    fireEvent.click(add);
    return q("[data-approved-here]");
  };

  it("approving a device here says so in Add devices, with no toast, and closes it after a few untouched seconds", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { onClose, release } = mountHost();
    const line = await approve();
    expect(line.textContent).toBe("✓macbook added: it may control mads on this pop-os. Add another, or Done.");
    expect(screen.getByRole("button", { name: "Done" })).toBeTruthy();
    expect(screen.queryByText("macbook added")).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_CLOSE_MS - 500);
    });
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    release();
  });

  it("touched after the approval, it stays open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { onClose, release } = mountHost();
    await approve();
    fireEvent.pointerDown(screen.getByRole("dialog", { name: "Add your devices" }).querySelector("[role=status]")!);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_CLOSE_MS * 2);
    });
    expect(onClose).not.toHaveBeenCalled();
    release();
  });
});

describe("Add devices, half-paired (F6 R5, R7)", () => {
  const mountAdd = (routes: Record<string, unknown>) => {
    const calls = stubRouter({
      "POST /devices/discoverable": { on: true },
      "GET /devices": [],
      ...routes,
    });
    render(<MeshSetup self={meshed()} rows={[]} host="MacBook-Air" step="add" onClose={() => {}} onUseCode={() => {}} />);
    return calls;
  };

  it("checks every pairing when it opens", async () => {
    const calls = mountAdd({ "GET /devices/discovered": found(), "POST /devices/check": { devices: [] } });
    await q('[data-found="spark"]');
    expect(calls.some((c) => c.method === "POST" && c.path === "/devices/check")).toBe(true);
  });

  it("none: Add, as now; both: Paired ✓ and no button", async () => {
    mountAdd({ "GET /devices/discovered": found({ pairing: "none" }) });
    expect(await screen.findByRole("button", { name: "Add spark" })).toBeTruthy();
    cleanup();
    resetSetupWindowForTests();
    mountAdd({ "GET /devices/discovered": found({ paired: true, pairing: "both" }) });
    const item = await q('[data-found="spark"]');
    await waitFor(() => expect(within(item).getByText("Paired ✓")).toBeTruthy());
    expect(within(item).queryByRole("button")).toBeNull();
  });

  it.each([
    ["here_only", "spark no longer has this MacBook-Air paired."],
    ["there_only", "spark still has this MacBook-Air from an earlier pairing."],
  ] as const)("%s: Re-pair, in Pair's style, with the reason in the warning colour; it sends the normal request", async (pairing, why) => {
    const calls = mountAdd({
      "GET /devices/discovered": found({ paired: pairing === "here_only", pairing }),
      [`POST /devices/discovered/${SPARK}/request`]: () => joinOf({ vouched: true, check_code: undefined }),
      "GET /devices/pair/KKKK%3Apr_1": () => joinOf({ vouched: true, check_code: undefined }),
    });
    const btn = await screen.findByRole("button", { name: "Re-pair spark" });
    expect(btn.textContent).toBe("Re-pair");
    expect(btn.className).toMatch(/primary/);
    const line = (await q(`[data-repair="${pairing}"]`)) as HTMLElement;
    expect(line.textContent).toBe(why);
    expect(line.className).toMatch(/repairWhy/);
    fireEvent.click(btn);
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path === `/devices/discovered/${SPARK}/request`)).toBe(true));
    expect(calls.find((c) => c.path === `/devices/discovered/${SPARK}/request`)!.body).toEqual({});
  });

  it("a retryable failure offers Try again, after Retry-After; another doesn't", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let n = 0;
    const calls = mountAdd({
      "GET /devices/discovered": found(),
      [`POST /devices/discovered/${SPARK}/request`]: () =>
        ++n === 1
          ? new Response(JSON.stringify({ code: "rate_limited", error: "x", retryable: true, retry_after: 3 }), { status: 429 })
          : new Response(JSON.stringify({ code: "pairing_refused", error: "that request is gone" }), { status: 403 }),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add spark" }));
    const item = await q('[data-found="spark"]');
    const again = (await within(item).findByRole("button", { name: /^Try again in \d s$/ })) as HTMLButtonElement;
    expect(again.disabled).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_500);
    });
    const now = within(item).getByRole("button", { name: "Try again" }) as HTMLButtonElement;
    expect(now.disabled).toBe(false);
    fireEvent.click(now);
    await waitFor(() => expect(within(item).getByRole("alert").getAttribute("data-refusal")).toBe("pairing_refused"));
    expect(within(item).queryByRole("button", { name: /Try again/ })).toBeNull();
    expect(calls.filter((c) => c.path === `/devices/discovered/${SPARK}/request`)).toHaveLength(2);
  });

  it("couldn't connect reads as that, with Try again", async () => {
    mountAdd({
      "GET /devices/discovered": found(),
      [`POST /devices/discovered/${SPARK}/request`]: () => new Response(JSON.stringify({ code: "dial_failed", error: "connection reset", retryable: true }), { status: 502 }),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add spark" }));
    const item = await q('[data-found="spark"]');
    await waitFor(() => expect(within(item).getByRole("alert").textContent).toBe("Couldn't reach spark."));
    expect(within(item).getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("the setup window (S2)", () => {
  function Probe({ on = true }: { on?: boolean }) {
    const [w] = useSetupWindow(on);
    return <span data-testid="w">{w.state}</span>;
  }
  const bodies = (calls: Call[]) => calls.filter((c) => c.path === "/devices/discoverable").map((c) => c.body);
  let hidden = false;
  const setHidden = (h: boolean) => {
    hidden = h;
    document.dispatchEvent(new Event("visibilitychange"));
  };
  beforeEach(() => {
    hidden = false;
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
  });
  afterEach(() => {
    delete (document as unknown as Record<string, unknown>).visibilityState;
  });

  it("opens, renews every minute, and closes on unmount, every call with a timeout", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/self": SELF });
    const { unmount } = render(<Probe />);
    await waitFor(() => expect(screen.getByTestId("w").textContent).toBe("open"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(RENEW_MS + 10);
    });
    unmount();
    await waitFor(() => expect(bodies(calls)).toEqual([{ on: true }, { on: true, renew: true }, { on: false }]));
    expect(calls.every((c) => c.signal instanceof AbortSignal)).toBe(true);
  });

  it("StrictMode's mount, unmount, mount opens it once", async () => {
    const calls = stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/self": SELF });
    render(
      <StrictMode>
        <Probe />
      </StrictMode>,
    );
    await waitFor(() => expect(screen.getByTestId("w").textContent).toBe("open"));
    expect(bodies(calls)).toEqual([{ on: true }]);
  });

  it("closed and opened again quickly: the calls land in order, open last", async () => {
    let release!: () => void;
    const slow = new Promise<void>((r) => (release = r));
    const seen: unknown[] = [];
    stubRouter({ "GET /devices/self": SELF });
    const routed = globalThis.fetch;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/devices/discoverable")) return routed(input, init);
      const body = JSON.parse(init!.body as string) as { on: boolean };
      seen.push(body);
      if (seen.length === 1) await slow; // the first open is slow to answer
      return new Response(JSON.stringify({ on: body.on }), { status: 200 });
    });
    const r = render(<Probe />);
    await waitFor(() => expect(seen).toHaveLength(1));
    r.rerender(<Probe on={false} />);
    await act(async () => {});
    r.rerender(<Probe on />);
    await act(async () => {});
    // Nothing else is sent while the first is in flight.
    expect(seen).toEqual([{ on: true }]);
    release();
    await waitFor(() => expect(seen).toEqual([{ on: true }, { on: false }, { on: true }]));
    await waitFor(() => expect(screen.getByTestId("w").textContent).toBe("open"));
  });

  it("a hidden page keeps it open but stops renewing; shown again, it renews at once", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/self": SELF });
    render(<Probe />);
    await waitFor(() => expect(bodies(calls)).toEqual([{ on: true }]));
    act(() => setHidden(true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(RENEW_MS * 3);
    });
    expect(bodies(calls)).toEqual([{ on: true }]);
    expect(screen.getByTestId("w").textContent).toBe("open");
    act(() => setHidden(false));
    await waitFor(() => expect(bodies(calls)).toEqual([{ on: true }, { on: true, renew: true }]));
  });

  it("shown again after it lapsed: closed, with the Router's reason", async () => {
    let lapsed = false;
    stubRouter({ "GET /devices/self": SELF });
    const routed = globalThis.fetch;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/devices/discoverable")) return routed(input, init);
      if (lapsed) return new Response(JSON.stringify({ code: "window_closed", error: "the setup window has closed: open it again" }), { status: 409 });
      return new Response(JSON.stringify({ on: true }), { status: 200 });
    });
    render(<Probe />);
    await waitFor(() => expect(screen.getByTestId("w").textContent).toBe("open"));
    act(() => setHidden(true));
    lapsed = true;
    act(() => setHidden(false));
    await waitFor(() => expect(screen.getByTestId("w").textContent).toBe("closed"));
  });

  it("leaving the page sends a close that outlives it (keepalive)", async () => {
    const calls = stubRouter({ "POST /devices/discoverable": { on: true }, "GET /devices/self": SELF });
    const fetchSpy = globalThis.fetch as unknown as { mock: { calls: [string, RequestInit][] } };
    render(<Probe />);
    await waitFor(() => expect(screen.getByTestId("w").textContent).toBe("open"));
    window.dispatchEvent(new Event("pagehide"));
    await waitFor(() => expect(bodies(calls)).toEqual([{ on: true }, { on: false }]));
    const last = fetchSpy.mock.calls[fetchSpy.mock.calls.length - 1][1];
    expect(last.keepalive).toBe(true);
    expect(last.signal).toBeInstanceOf(AbortSignal);
  });
});

describe("polls (S3)", () => {
  let hidden = false;
  beforeEach(() => {
    hidden = false;
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
  });
  afterEach(() => {
    delete (document as unknown as Record<string, unknown>).visibilityState;
  });

  it("the found list pauses while the page is hidden, backs off on errors, and stops on close", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let fail = false;
    const calls = stubRouter({
      "POST /devices/discoverable": { on: true },
      "GET /devices/discovered": () => (fail ? new Response(JSON.stringify({ error: "down" }), { status: 502 }) : found()),
    });
    const reads = () => calls.filter((c) => c.path === "/devices/discovered").length;
    const { unmount } = render(<MeshSetup self={meshed()} rows={[]} host="pop-os" step="add" onClose={() => {}} onUseCode={() => {}} />);
    await waitFor(() => expect(reads()).toBe(1));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DISCOVERED_POLL_MS + 10);
    });
    expect(reads()).toBe(2);
    // Hidden: nothing.
    hidden = true;
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DISCOVERED_POLL_MS * 10);
    });
    expect(reads()).toBe(2);
    // Shown: a read at once; then failures double the wait.
    fail = true;
    hidden = false;
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    await waitFor(() => expect(reads()).toBe(3));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DISCOVERED_POLL_MS * 2 + 10);
    });
    expect(reads()).toBe(4);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DISCOVERED_POLL_MS * 2 + 10);
    });
    expect(reads()).toBe(4);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DISCOVERED_POLL_MS * 2);
    });
    expect(reads()).toBe(5);
    unmount();
    const n = reads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(reads()).toBe(n);
  });

  it("the card re-reads slowly once Reachable (so Not answering shows without a reload), and not while hidden (T4)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let answering = true;
    const calls = stubRouter({ "GET /devices/self": () => meshed({ service_answering: answering }) });
    const reads = () => calls.filter((c) => c.path === "/devices/self").length;
    function Live() {
      const s = useSelfDevice().value;
      return s ? <MeshNetworkCard self={s} host="pop-os" onChange={() => {}} onToast={() => {}} /> : null;
    }
    render(<Live />);
    await waitFor(() => expect(document.querySelector("[data-mesh-state]")?.textContent).toBe("Reachable"));
    const before = reads();
    answering = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(REACHABLE_POLL_MS + 10);
    });
    expect(reads()).toBe(before + 1);
    await waitFor(() => expect(document.querySelector("[data-mesh-state]")?.textContent).toBe("Not answering"));
    hidden = true;
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    const n = reads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STATE_POLL_MS * 20);
    });
    expect(reads()).toBe(n);
  });
});

describe("the Mesh network card", () => {
  const card = (self: SelfDevice, routes: Record<string, unknown> = {}) => {
    const calls = stubRouter({ "PUT /devices/settings": (c: Call) => ({ ...self, mesh: { ...self.mesh!, ...(c.body as object) } }), ...routes });
    const onToast = vi.fn();
    render(<MeshNetworkCard self={self} host="pop-os" onChange={() => {}} onToast={onToast} now={new Date("2026-11-23T12:00:00Z")} />);
    return { calls, onToast };
  };

  it("names the network, its meaning, and its state; Advanced options has the check (on), the internet (off) and the port", () => {
    const { calls } = card(meshed());
    expect(document.querySelector("[data-mesh-network]")!.textContent).toContain("Tailscale");
    expect(document.querySelector("[data-mesh-state]")!.textContent).toBe("Reachable");
    fireEvent.click(screen.getByText("Advanced options"));
    expect(screen.getByRole("switch", { name: "Check Tailscale identity" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByText("Only accept devices signed in to your tailnet as you.")).toBeTruthy();
    expect(screen.getByRole("switch", { name: "Also reachable from the internet" }).getAttribute("aria-checked")).toBe("false");
    expect(screen.getByText("17380")).toBeTruthy();
    // The internet asks first.
    fireEvent.click(screen.getByRole("switch", { name: "Also reachable from the internet" }));
    const confirm = screen.getByRole("alertdialog", { name: "Also reachable from the internet?" });
    expect(confirm.textContent).toContain("scanners find open ports within hours");
    fireEvent.click(within(confirm).getByRole("button", { name: "Make it reachable" }));
    return waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ public: true, confirm_public: true }));
  });

  it("Tailscale: the key-expiry date, expiring soon, and Sign in to Tailscale only when it needs it", async () => {
    const ts = { present: true, needs_login: false, key_expiry_off: false, admin_url: "https://login.tailscale.com/admin/machines" };
    const { unmount } = render(<MeshNetworkCard self={meshed({ tailscale: { ...ts, key_expiry: "2027-03-03T10:00:00Z" } })} host="pop-os" onChange={() => {}} onToast={() => {}} now={new Date("2026-11-23T12:00:00Z")} />);
    expect(document.querySelector('[data-tailscale="expiry"]')!.textContent).toContain("Tailscale signs this pop-os out on Mar 3");
    expect(screen.getByRole("link", { name: "Open Tailscale admin" }).getAttribute("href")).toBe("https://login.tailscale.com/admin/machines");
    unmount();
    const r2 = render(<MeshNetworkCard self={meshed({ tailscale: { ...ts, key_expiry: "2026-11-28T10:00:00Z" } })} host="pop-os" onChange={() => {}} onToast={() => {}} now={new Date("2026-11-23T12:00:00Z")} />);
    expect(document.querySelector('[data-tailscale="expiring"]')!.textContent).toContain("Tailscale signs this pop-os out in 5 days");
    r2.unmount();
    const open = vi.fn();
    vi.stubGlobal("open", open);
    const { calls } = card(meshed({ tailscale: { ...ts, needs_login: true } }), {
      "POST /devices/tailscale/login": { auth_url: "https://login.tailscale.com/a/abc", needs_login: true },
      "GET /devices/self": meshed(),
    });
    expect(document.querySelector("[data-mesh-state]")!.textContent).toBe("Signed out");
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Tailscale" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith("https://login.tailscale.com/a/abc", "_blank", "noreferrer"));
    expect(calls.filter((c) => c.path === "/devices/tailscale/login")).toHaveLength(1);
  });

  it("a sign-in link that isn't Tailscale's is never opened (S4)", async () => {
    const open = vi.fn();
    vi.stubGlobal("open", open);
    const ts = { present: true, needs_login: true, key_expiry_off: false, admin_url: "javascript:alert(1)" };
    const { onToast } = card(meshed({ tailscale: ts }), {
      "POST /devices/tailscale/login": { auth_url: "javascript:alert(1)", needs_login: true },
      "GET /devices/self": meshed(),
    });
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Tailscale" }));
    await waitFor(() => expect(onToast).toHaveBeenCalledWith(expect.objectContaining({ tone: "bad" })));
    expect(open).not.toHaveBeenCalled();
  });

  it("a sign-in link the Router refused says so, and opens nothing", async () => {
    const open = vi.fn();
    vi.stubGlobal("open", open);
    const { onToast } = card(meshed({ tailscale: { present: true, needs_login: true, key_expiry_off: false, admin_url: "" } }), {
      "POST /devices/tailscale/login": () => new Response(JSON.stringify({ code: "unexpected_auth_url", error: "Tailscale gave an unexpected sign-in link: sign in from the Tailscale app instead" }), { status: 502 }),
      "GET /devices/self": meshed(),
    });
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Tailscale" }));
    await waitFor(() => expect(onToast).toHaveBeenCalledWith(expect.objectContaining({ tone: "bad", title: "Tailscale gave an unexpected sign-in link" })));
    expect(open).not.toHaveBeenCalled();
  });

  it("admin links are always Tailscale's own console", () => {
    card(meshed({ tailscale: { present: true, needs_login: false, key_expiry: "2026-11-28T10:00:00Z", key_expiry_off: false, admin_url: "javascript:alert(1)" } }));
    expect(screen.getByRole("link", { name: "Open Tailscale admin" }).getAttribute("href")).toBe("https://login.tailscale.com/admin/machines");
  });

  it("this device not on its network now: the card says what that means", () => {
    card(meshed({ networks: [{ ...TS, chosen: true, available: false }] }));
    expect(document.querySelector("[data-mesh-state]")!.textContent).toBe("Not on it now");
    expect(document.querySelector("[data-mesh-problem]")!.textContent).toContain("This pop-os isn't on Tailscale right now.");
  });
});

describe("Troubleshoot connection", () => {
  const addr = (o: { addr: string; state: string; label?: string; kind?: "lan" | "overlay"; network?: string }) => ({ via: "direct", source: "hello", added_at: new Date().toISOString(), ...o });
  const OFFLINE: DeviceRow = {
    ...STUDIO,
    reachability: "unreachable",
    addresses: [addr({ addr: "100.101.4.7:17380", kind: "overlay", label: "Tailscale", state: "not_answering" }), addr({ addr: "192.168.1.20:17380", kind: "lan", label: "Home", state: "other_device" })],
  };

  it("opens by itself on a problem (the row with it), and holds the only addresses on the page", async () => {
    mountSection(meshed(), [OFFLINE, { ...STUDIO, id: "per_ok", name: "ok-box", fingerprint: fp("O"), path: { addr: "100.1.1.1:17380", label: "Tailscale", since: "" } }]);
    const row = await q('[data-device="mac-studio"]');
    expect(row.getAttribute("aria-expanded")).toBe("true");
    expect(row.hasAttribute("data-problem")).toBe(true);
    expect(document.querySelector('[data-device="ok-box"]')!.getAttribute("aria-expanded")).toBe("false");
    const details = document.querySelector("details:has([data-troubleshoot])") as HTMLDetailsElement;
    expect(details.open).toBe(true);
    const diag = document.querySelector('[data-diagnosis="unreachable"]')!;
    expect(diag.textContent).toContain("mac-studio isn't answering");
    expect(diag.textContent).toContain("A different device answered on Home.");
    // Addresses only in the path list, never in the diagnosis.
    expect(diag.textContent).not.toContain("192.168");
    // Both tried networks: Tailscale and a LAN, so the Wi-Fi tip applies.
    expect(diag.textContent).toContain("Does this Wi-Fi stop devices");
    expect(diag.textContent).toContain("port 17380");
    // Addresses only under Troubleshoot, never in the row.
    expect(row.textContent).not.toContain("100.101");
    expect(document.querySelector("[data-paths]")!.textContent).toContain("100.101.4.7:17380");
  });

  it("Not reachable from this network: what each uses, and what to do", async () => {
    mountSection(meshed({ networks: [{ ...TS, chosen: true, available: false, listening: false }, CAMPUS] }), [
      { ...OFFLINE, reachability: "no_shared_network", addresses: [addr({ addr: "100.101.4.7:17380", label: "Tailscale", network: "tailscale", kind: "overlay", state: "not_on_network" })] },
    ]);
    const diag = await q('[data-diagnosis="no_shared_network"]');
    expect(diag.textContent).toContain("mac-studio uses Tailscale. This pop-os is on Wi-Fi “eduroam” right now.");
    expect(diag.textContent).toContain("Turn on Tailscale on this pop-os.");
    expect(document.querySelector('[data-address-state="not_on_network"]')!.textContent).toContain("This pop-os isn't on it now");
  });

  it("Tailscale expiring opens it with the admin console link; Try again now re-dials", async () => {
    const calls = mountSection(
      meshed(),
      [{ ...STUDIO, diagnosis: "tailscale_key_expiring", remote: { ...STUDIO.remote!, tailscale_key_expiry: new Date(Date.now() + 5 * 86_400_000 - 60_000).toISOString() } }],
      { [`POST /devices/${STUDIO.fingerprint}/redial`]: { reachability: "connected", path: { label: "Tailscale" } } },
    );
    const diag = await q('[data-diagnosis="tailscale_key_expiring"]');
    expect(diag.textContent).toContain("mac-studio's Tailscale sign-in expires in 5 days");
    expect(within(diag).getByRole("link").getAttribute("href")).toBe("https://login.tailscale.com/admin/machines");
    fireEvent.click(screen.getByRole("button", { name: "Try again now" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path.endsWith("/redial"))).toBe(true));
    expect(await screen.findByText("Connected via Tailscale.")).toBeTruthy();
  });
});

describe("Troubleshoot, smaller review items", () => {
  const addr = (o: { addr: string; state: string; label?: string; kind?: "lan" | "overlay" }) => ({ via: "direct", source: "hello", added_at: new Date().toISOString(), ...o });
  it("Offline over Tailscale alone gives no Wi-Fi tip", async () => {
    mountSection(meshed(), [{ ...STUDIO, reachability: "unreachable", addresses: [addr({ addr: "100.1.1.1:17380", label: "Tailscale", kind: "overlay", state: "not_answering" })] }]);
    const diag = await q('[data-diagnosis="unreachable"]');
    expect(diag.textContent).not.toContain("Wi-Fi");
  });
  it("stays open when the problem clears, and opens again when one comes", async () => {
    const off = { ...STUDIO, reachability: "unreachable" as const, addresses: [addr({ addr: "100.1.1.1:17380", label: "Tailscale", kind: "overlay", state: "not_answering" })] };
    const { rerender } = render(<TroubleshootConnection row={off} host="pop-os" mesh={meshed().mesh} onPaste={() => {}} onToast={() => {}} />);
    const details = () => document.querySelector("details") as HTMLDetailsElement;
    expect(details().open).toBe(true);
    rerender(<TroubleshootConnection row={{ ...STUDIO, path: { addr: "100.1.1.1:17380", label: "Tailscale", since: new Date().toISOString() } }} host="pop-os" mesh={meshed().mesh} onPaste={() => {}} onToast={() => {}} />);
    expect(details().open).toBe(true);
    details().open = false;
    rerender(<TroubleshootConnection row={off} host="pop-os" mesh={meshed().mesh} onPaste={() => {}} onToast={() => {}} />);
    expect(details().open).toBe(true);
  });
  it("says since when, today or not", () => {
    const now = new Date(2026, 10, 23, 12, 0);
    expect(connectedSince(new Date(2026, 10, 23, 8, 12).toISOString(), now)).toBe("Connected since 08:12");
    expect(connectedSince(new Date(2026, 10, 21, 8, 12).toISOString(), now)).toBe("Connected since Nov 21, 08:12");
    expect(connectedSince("0001-01-01T00:00:00Z", now)).toBe("Connected");
    expect(connectedSince(undefined, now)).toBe("Connected");
  });
});

describe("the network list (smaller items)", () => {
  it("never preselects a shared network, and a new pick clears the last one's error", async () => {
    stubRouter({ "PUT /devices/settings": () => new Response(JSON.stringify({ code: "gateway_unknown", error: "x" }), { status: 422 }) });
    render(<MeshSetup self={fresh({ networks: [CAMPUS, HOME] })} rows={[]} host="pop-os" step="choose" onClose={() => {}} onUseCode={() => {}} />);
    expect(screen.getByRole("radio", { name: /eduroam/ }).getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("radio", { name: /Kidwell-5G/ }).getAttribute("aria-checked")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("radio", { name: /eduroam/ }));
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("Paste a pairing code, joiner-first", () => {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  const code = (over: object = {}) =>
    `mads-pair-2:${b64({ n: "pop-os", f: SPARK, e: Math.floor(Date.now() / 1000) + 300, s: btoa("x".repeat(32)).replace(/=+$/, ""), a: [{ v: "direct", h: "100.101.4.7:17380", k: "overlay", l: "Tailscale" }, { v: "direct", h: "192.168.1.20:17380", k: "lan", l: "Home" }], ...over })}`;

  it("names the networks, not the addresses; reachable here adopts it", () => {
    stubRouter({});
    render(<PasteCode self={fresh()} rows={[]} onJoined={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    const line = document.querySelector("[data-code-networks]")!;
    expect(line.textContent).toBe("This pop-os can reach pop-os over Tailscale. Pairing makes that this pop-os's mesh network.");
    expect(document.body.textContent).not.toContain("100.101");
  });

  it("no shared network: the tips, and Check again (the code isn't used up)", async () => {
    let n = 0;
    const calls = stubRouter({
      "POST /devices/pair": () =>
        ++n === 1
          ? new Response(JSON.stringify({ code: "no_shared_network", error: "x", host_networks: ["Tailscale", "Home"], tips: "no_shared_network" }), { status: 422 })
          : joinOf({ mode: "code", check_code: undefined }),
    });
    const onJoined = vi.fn();
    render(<PasteCode self={fresh({ networks: [CAMPUS] })} rows={[]} onJoined={onJoined} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    const tips = await q('[data-tips="no_shared_network"]');
    expect(tips.textContent).toContain("pop-os is reachable over Tailscale and Home. This pop-os is on neither right now.");
    fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(onJoined).toHaveBeenCalled());
    expect(calls.filter((c) => c.path === "/devices/pair")).toHaveLength(2);
  });

  it("an already-paired host's code updates its addresses", async () => {
    const calls = stubRouter({
      "POST /devices/pair": (c: Call) =>
        (c.body as { update_addresses?: boolean }).update_addresses
          ? { update_addresses: true, peer_id: "per_spark", name: "spark" }
          : new Response(JSON.stringify({ code: "already_paired_update", error: "x", name: "spark", peer_id: "per_spark" }), { status: 409 }),
      "GET /devices": [],
    });
    const onUpdated = vi.fn();
    render(<PasteCode self={meshed()} rows={[]} onJoined={() => {}} onCancel={() => {}} onUpdated={onUpdated} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    fireEvent.click(await screen.findByRole("button", { name: "Update addresses" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledWith("spark"));
    expect(calls.filter((c) => c.path === "/devices/pair").map((c) => (c.body as { update_addresses?: boolean }).update_addresses)).toEqual([undefined, true]);
  });
});
