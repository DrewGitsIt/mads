import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { resetDevicesForTests, type DeviceRow } from "../../../router/devices";
import { resetMeshForTests } from "../../../router/mesh";
import { PairedDevicesSection } from "./PairedDevicesSection";
import { resetSetupWindowForTests } from "./paired/setup/useSetup";
import { clearPairOutcome, PairRequestWatcher } from "./paired/pairing/PairRequestWatcher";
import { ToastViewport } from "../../../components/Toast/Toast";
import { DISPLAY, MAC, PHONE, row, SELF, STUDIO, stubRouter, type Call } from "./paired/testRouter";

const ACTIVITY = [
  { seq: 3, timestamp: new Date().toISOString(), type: "device.grant_changed", actor: "router", payload: { fingerprint: MAC.fingerprint, level: "control", previous_level: "admin" } },
  { seq: 2, timestamp: new Date().toISOString(), type: "device.paired", actor: "router", payload: { fingerprint: PHONE.fingerprint, level: "chat", how: "code" } },
];

function mount(rows: DeviceRow[] = [MAC, PHONE, DISPLAY], self = SELF, extra: Record<string, unknown> = {}): Call[] {
  const calls = stubRouter({
    "GET /devices": rows,
    "GET /devices/self": self,
    "GET /devices/activity": (c: Call) => (c.path.includes("device=") ? ACTIVITY.slice(1) : ACTIVITY),
    "GET /devices/pair-requests": [],
    "GET /peers": [],
    [`PUT /devices/${MAC.fingerprint}/grant`]: {},
    [`PUT /devices/${PHONE.fingerprint}/grant`]: {},
    [`PUT /devices/${DISPLAY.fingerprint}/grant`]: {},
    "PUT /devices/settings": (c: Call) => ({ ...self, ...(c.body as object) }),
    "POST /peers": {},
    [`DELETE /peers/${STUDIO.name}`]: null,
    ...extra,
  });
  render(<PairedDevicesSection />);
  return calls;
}

const CODE = { id: "pc_1", code: "mads-pair-2:abc", expires_at: new Date(Date.now() + 300_000).toISOString(), host: { name: "pop-os", fingerprint: SELF.fingerprint } };

const rowOf = (name: string) => document.querySelector(`[data-device="${name}"]`) as HTMLElement;
const puts = (calls: Call[]) => calls.filter((c) => c.method === "PUT");

describe("PairedDevicesSection", () => {
  afterEach(() => {
    resetDevicesForTests();
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  it("shows each device in both directions, with the status worded by direction", async () => {
    mount();
    await waitFor(() => expect(rowOf("Alexs-MacBook-Air")).toBeTruthy());
    expect(screen.getByRole("columnheader", { name: "On this pop-os, it may" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "It lets this pop-os" })).toBeTruthy();
    const mac = within(rowOf("Alexs-MacBook-Air"));
    expect(mac.getByRole("button", { name: "What Alexs-MacBook-Air may do with mads on this pop-os" }).textContent).toBe("control");
    expect(rowOf("Alexs-MacBook-Air").querySelector("[data-lets]")!.textContent).toBe("not use");
    expect(mac.getByText("Reachable")).toBeTruthy();
    expect(rowOf("Alexs-MacBook-Air").querySelector("[data-dot]")!.getAttribute("data-dot")).toBe("muted");
    const phone = within(rowOf("Drews-iPhone"));
    expect(phone.getByText("Last used this pop-os 5 min ago")).toBeTruthy();
    expect(within(rowOf("kitchen-display")).getByText("Never used this pop-os")).toBeTruthy();
    // Every status and every "Lets" has its explanation.
    const tips = screen.getAllByRole("tooltip").map((t) => t.textContent);
    expect(tips).toContain("Drews-iPhone doesn't host mads sessions, so there's nothing it could let this pop-os do.");
    expect(tips.some((t) => t?.startsWith("Alexs-MacBook-Air answers, but lets this pop-os not use mads there."))).toBe(true);
  });

  it("disables Show sessions with the reason, when there's nothing to show", async () => {
    mount();
    await waitFor(() => expect(rowOf("Drews-iPhone")).toBeTruthy());
    const sw = within(rowOf("Drews-iPhone")).getByRole("switch", { name: "Show mads sessions from Drews-iPhone" });
    expect((sw as HTMLButtonElement).disabled).toBe(true);
    expect(within(rowOf("Drews-iPhone")).getAllByRole("tooltip").map((t) => t.textContent)).toContain("Drews-iPhone doesn't host mads sessions.");
    expect((within(rowOf("Alexs-MacBook-Air")).getByRole("switch") as HTMLButtonElement).disabled).toBe(true);
  });

  it("Show sessions uses the /peers routes", async () => {
    const calls = mount([STUDIO]);
    await waitFor(() => expect(rowOf("mac-studio")).toBeTruthy());
    fireEvent.click(within(rowOf("mac-studio")).getByRole("switch"));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.path === "/peers/mac-studio")).toBe(true));
  });

  it("opens a row's details below it, one at a time; a second click or Esc closes them", async () => {
    mount();
    await waitFor(() => expect(rowOf("Drews-iPhone")).toBeTruthy());
    fireEvent.click(within(rowOf("Drews-iPhone")).getByText("Drews-iPhone"));
    expect(rowOf("Drews-iPhone").getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("Messages to Full access sessions")).toBeTruthy();
    expect(screen.getByLabelText(/^Fingerprint PPPP/)).toBeTruthy();
    fireEvent.click(within(rowOf("kitchen-display")).getByText("kitchen-display"));
    expect(rowOf("Drews-iPhone").getAttribute("aria-expanded")).toBe("false");
    expect(rowOf("kitchen-display").getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(within(rowOf("kitchen-display")).getByText("kitchen-display"));
    expect(rowOf("kitchen-display").getAttribute("aria-expanded")).toBe("false");
    fireEvent.keyDown(rowOf("kitchen-display"), { key: "Enter" });
    expect(rowOf("kitchen-display").getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(rowOf("kitchen-display").getAttribute("aria-expanded")).toBe("false");
    // Its own controls don't toggle it.
    fireEvent.click(within(rowOf("kitchen-display")).getByRole("button", { name: /What kitchen-display may do/ }));
    expect(rowOf("kitchen-display").getAttribute("aria-expanded")).toBe("false");
  });

  it("saves a lowered level at once; raising to control asks first", async () => {
    const calls = mount();
    await waitFor(() => expect(rowOf("Alexs-MacBook-Air")).toBeTruthy());
    fireEvent.click(within(rowOf("Alexs-MacBook-Air")).getByRole("button", { name: /What Alexs-MacBook-Air may do/ }));
    const admin = screen.getByRole("option", { name: /administer/ });
    expect(admin.textContent).toContain("Set here only");
    fireEvent.click(screen.getByRole("option", { name: /^view/ }));
    await waitFor(() => expect(puts(calls)).toEqual([expect.objectContaining({ path: `/devices/${MAC.fingerprint}/grant`, body: { level: "view" } })]));

    fireEvent.click(within(rowOf("kitchen-display")).getByRole("button", { name: /What kitchen-display may do/ }));
    fireEvent.click(screen.getByRole("option", { name: /^control/ }));
    const confirm = screen.getByRole("alertdialog", { name: "Let kitchen-display control mads on this pop-os?" });
    expect(confirm.textContent).toContain("kitchen-display will be able to start Full access sessions, run a shell, and commit, land and push on pop-os.");
    expect(puts(calls)).toHaveLength(1);
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }));
    expect(puts(calls)).toHaveLength(1);

    fireEvent.click(within(rowOf("kitchen-display")).getByRole("button", { name: /What kitchen-display may do/ }));
    fireEvent.click(screen.getByRole("option", { name: /^administer/ }));
    const raise = screen.getByRole("alertdialog", { name: "Let kitchen-display administer mads on this pop-os?" });
    expect(raise.textContent).toContain("Administer can only be given here");
    fireEvent.click(within(raise).getByRole("button", { name: "Let it administer" }));
    await waitFor(() => expect(puts(calls)[1]).toMatchObject({ path: `/devices/${DISPLAY.fingerprint}/grant`, body: { level: "admin" } }));
  });

  it("sets a device's limits: C1 only at chat with, the trust ceiling only at control or administer", async () => {
    const calls = mount();
    await waitFor(() => expect(rowOf("Drews-iPhone")).toBeTruthy());
    fireEvent.click(within(rowOf("Drews-iPhone")).getByText("Drews-iPhone"));
    const allowed = screen.getByRole("radio", { name: "Allowed" }) as HTMLButtonElement;
    expect(screen.getByRole("radio", { name: "Refused" }).getAttribute("aria-checked")).toBe("true");
    expect(allowed.disabled).toBe(false);
    expect((screen.getByLabelText("Highest session trust") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Only matters at control or administer.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "About Messages to Full access sessions" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "About Highest session trust" })).toBeTruthy();
    fireEvent.click(allowed);
    await waitFor(() =>
      expect(puts(calls)[0]).toMatchObject({ path: `/devices/${PHONE.fingerprint}/grant`, body: { level: "chat", ceilings: { full_trust_chat: "allowed" } } }),
    );
  });

  it("Introduce a device… shows only once a device lets this one administer it", async () => {
    mount();
    await waitFor(() => expect(rowOf("Alexs-MacBook-Air")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Introduce a device…" })).toBeNull();
    expect(screen.getByRole("button", { name: "Pair with a code" })).toBeTruthy();
  });

  it("Introduce a device… offers view to control, to the hosts this one administers", async () => {
    const calls = mount([MAC, PHONE, STUDIO]);
    await waitFor(() => expect(rowOf("mac-studio")).toBeTruthy());
    const intro = screen.getByRole("button", { name: "Introduce a device…" });
    expect(intro.getAttribute("aria-disabled")).toBeNull();
    fireEvent.click(intro);
    const dialog = screen.getByRole("dialog", { name: "Introduce a device" });
    const radios = within(dialog).getAllByRole("radio").map((r) => r.textContent);
    expect(radios).toEqual(["view", "chat with", "control"]);
    expect(dialog.textContent).toContain("Up to control. Administer is only set on mac-studio itself.");
    expect(calls.length).toBeGreaterThan(0);
  });

  it("the section's Device activity log filters by device", async () => {
    const calls = mount();
    await waitFor(() => expect(screen.getByText(/changed from administer to control/)).toBeTruthy());
    fireEvent.click(document.getElementById("device-activity-filter")!);
    fireEvent.click(screen.getByRole("option", { name: "Drews-iPhone" }));
    await waitFor(() => expect(calls.some((c) => c.path.startsWith(`/devices/activity?device=${PHONE.fingerprint}`))).toBe(true));
    await waitFor(() => expect(screen.queryByText(/changed from administer to control/)).toBeNull());
  });

  it("says when nothing is paired", async () => {
    mount([]);
    await waitFor(() => expect(screen.getByText("No devices yet")).toBeTruthy());
  });

  it("this device's short fingerprint opens to the whole one, slashed zeros and all", async () => {
    mount();
    const chip = await screen.findByRole("button", { name: "Fingerprint SSSS-SSSS-SSSS-SSSS" });
    fireEvent.click(chip);
    expect(screen.getByRole("dialog", { name: "pop-os fingerprint" }).textContent).toContain("Other devices check it when you pair.");
  });

  it("one fingerprint format everywhere: hyphenated groups, 7 to a line, the chip's text its start (Q9)", async () => {
    const lines = (el: Element) => [...el.querySelectorAll("[aria-label^='Fingerprint '] > div")].map((d) => d.textContent);
    const mac = MAC.fingerprint!.match(/.{4}/g)!;
    const want = [mac.slice(0, 7).join("-") + "-", mac.slice(7).join("-")];
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Fingerprint SSSS-SSSS-SSSS-SSSS" }));
    const pop = screen.getByRole("dialog", { name: "pop-os fingerprint" });
    expect(lines(pop)[0]!.startsWith("SSSS-SSSS-SSSS-SSSS-")).toBe(true);
    expect(pop.querySelectorAll("[data-short]").length).toBe(4);
    // A row's details: the same lines.
    fireEvent.click(within(rowOf("Alexs-MacBook-Air")).getByText("Alexs-MacBook-Air"));
    const details = await waitFor(() => {
      const el = document.getElementById(`detail-${MAC.id}`);
      if (!el) throw new Error("no details");
      return el;
    });
    expect(lines(details)).toEqual(want);
    expect(details.textContent).toContain("Compare with the fingerprint next to This device on Alexs-MacBook-Air.");
  });
});

describe("half-paired devices (F6)", () => {
  afterEach(() => {
    cleanup();
    resetSetupWindowForTests();
    resetDevicesForTests();
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  const NOT_PAIRED: DeviceRow = { ...STUDIO, pairing_there: "not_paired", pairing_checked_at: new Date().toISOString() };
  // The row, once it's there.
  const rowSoon = (name: string) =>
    waitFor(() => {
      const r = rowOf(name);
      if (!r) throw new Error(`no row ${name}`);
      return r;
    });
  const list = (devices: { fingerprint: string }[]) => ({ searching: true, started_at: "", networks: [], devices, hidden_count: 0, hidden_known: false });

  it("opening the section checks every pairing, and shows what the check answered", async () => {
    // GET /devices carries the check's result once it ran, as the Router keeps it.
    let checked = false;
    const calls = mount([STUDIO], SELF, {
      "GET /devices": () => (checked ? [NOT_PAIRED] : [STUDIO]),
      "POST /devices/check": () => {
        checked = true;
        return { devices: [NOT_PAIRED] };
      },
    });
    await waitFor(() => expect(within(rowOf("mac-studio")).getByText("No longer paired on mac-studio")).toBeTruthy());
    expect(calls.filter((c) => c.method === "POST" && c.path === "/devices/check").length).toBeGreaterThan(0);
  });

  it("an older Router without the check: the rows stand as read", async () => {
    mount([STUDIO]);
    await waitFor(() => expect(within(rowOf("mac-studio")).getByText("Connected")).toBeTruthy());
  });

  it("a viewed row no longer paired there: the status in the warning colour, and Re-pair in Pair's style", async () => {
    mount([NOT_PAIRED]);
    const r = await rowSoon("mac-studio");
    expect(r.querySelector("[data-dot]")!.getAttribute("data-dot")).toBe("warn");
    const btn = within(r).getByRole("button", { name: "Re-pair mac-studio" });
    expect(btn.textContent).toBe("Re-pair");
    expect(btn.className).toMatch(/primary/);
    expect(within(r).getAllByRole("tooltip").map((t) => t.textContent)).toContain(
      "mac-studio no longer has this pop-os paired, so it refuses this pop-os. Re-pair keeps this row as it is; only mac-studio's side is new.",
    );
  });

  it("a row this device doesn't view gets no Re-pair", async () => {
    mount([{ ...NOT_PAIRED, viewed: false }]);
    const r = await rowSoon("mac-studio");
    expect(within(r).queryByRole("button", { name: "Re-pair mac-studio" })).toBeNull();
    expect(within(r).queryByText("No longer paired on mac-studio")).toBeNull();
  });

  it("Re-pair opens Add devices when the device is in the found list", async () => {
    mount([NOT_PAIRED], SELF, {
      "GET /devices/discovered": list([{ ...{ name: "mac-studio", short: "", network: "tailscale", via: "tailscale", paired: true, pairing: "here_only", compatible: true, seen_at: "" }, fingerprint: STUDIO.fingerprint! }]),
      "POST /devices/discoverable": { on: true },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Re-pair mac-studio" }));
    expect(await screen.findByRole("dialog", { name: "Add your devices" })).toBeTruthy();
  });

  it("Re-pair opens this device's pairing code when it isn't found", async () => {
    mount([NOT_PAIRED], SELF, { "GET /devices/discovered": list([]), "POST /devices/pairing-code": CODE });
    fireEvent.click(await screen.findByRole("button", { name: "Re-pair mac-studio" }));
    expect(await screen.findByRole("dialog", { name: "Copy this device's pairing code" })).toBeTruthy();
  });

  it("a different key answered: said as that, never Re-pair", async () => {
    mount([{ ...STUDIO, pairing_there: "other_key" }]);
    const r = await rowSoon("mac-studio");
    expect(within(r).getByText("A different key answered")).toBeTruthy();
    expect(r.querySelector("[data-dot]")!.getAttribute("data-dot")).toBe("bad");
    expect(within(r).queryByRole("button", { name: /Re-pair/ })).toBeNull();
  });

  it("a device with no address: says so, not that it doesn't host mads sessions", async () => {
    const lost = row({ name: "spark", fingerprint: "K".repeat(52), viewed: false, grant: { level: "view" }, remote: { platform: "linux", hosts_sessions: true } });
    mount([lost, PHONE]);
    const r = await rowSoon("spark");
    const tips = within(r).getAllByRole("tooltip").map((t) => t.textContent);
    const line = "This pop-os has no address for spark: pair again, or run mads mesh address add.";
    expect(tips).toContain(line);
    expect(tips.some((t) => t?.includes("doesn't host mads"))).toBe(false);
    // A phone still doesn't host them.
    expect(within(rowOf("Drews-iPhone")).getAllByRole("tooltip").map((t) => t.textContent)).toContain("Drews-iPhone doesn't host mads sessions.");
  });
});

describe("pairing codes in one click (P3)", () => {
  afterEach(() => {
    resetDevicesForTests();
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  it("Pair with a code opens straight to this device's code, already copied", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    mount([MAC], SELF, { "POST /devices/pairing-code": CODE });
    fireEvent.click(await screen.findByRole("button", { name: "Pair with a code" }));
    const dialog = await screen.findByRole("dialog", { name: "Copy this device's pairing code" });
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("mads-pair-2:abc"));
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Copy again" })).toBeTruthy());
    expect(dialog.textContent).toContain("Copied. Works once");
    // The other way round is one link away.
    fireEvent.click(within(dialog).getByRole("button", { name: "Paste it here" }));
    expect(await screen.findByRole("dialog", { name: "Paste a pairing code" })).toBeTruthy();
  });

  it("a clipboard that refuses leaves Copy as it was", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: () => Promise.reject(new Error("no focus")) } });
    mount([MAC], SELF, { "POST /devices/pairing-code": CODE });
    fireEvent.click(await screen.findByRole("button", { name: "Pair with a code" }));
    await waitFor(() => expect(screen.getByLabelText("Pairing code").textContent).toBe("mads-pair-2:abc"));
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  });

  const paste = (target: EventTarget, text: string) => {
    const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    e.clipboardData = { getData: () => text };
    target.dispatchEvent(e);
    return e;
  };

  it("pasting a pairing code anywhere on the section opens Paste a pairing code with it", async () => {
    mount([MAC]);
    await waitFor(() => expect(rowOf("Alexs-MacBook-Air")).toBeTruthy());
    const e = paste(rowOf("Alexs-MacBook-Air"), "  mads-pair-2:xyz\n");
    expect(e.defaultPrevented).toBe(true);
    const dialog = await screen.findByRole("dialog", { name: "Paste a pairing code" });
    expect((within(dialog).getByRole("textbox") as HTMLTextAreaElement).value).toBe("mads-pair-2:xyz");
  });

  it("other text, or a paste into a field, is left alone", async () => {
    mount([MAC]);
    await waitFor(() => expect(rowOf("Alexs-MacBook-Air")).toBeTruthy());
    expect(paste(rowOf("Alexs-MacBook-Air"), "hello").defaultPrevented).toBe(false);
    const field = document.createElement("input");
    rowOf("Alexs-MacBook-Air").querySelector("td")!.appendChild(field);
    expect(paste(field, "mads-pair-2:xyz").defaultPrevented).toBe(false);
    // Outside the section (another Settings section's control): not ours.
    const elsewhere = document.createElement("button");
    document.body.appendChild(elsewhere);
    expect(paste(elsewhere, "mads-pair-2:xyz").defaultPrevented).toBe(false);
    elsewhere.remove();
    expect(screen.queryByRole("dialog", { name: "Paste a pairing code" })).toBeNull();
  });
});

describe("a pairing answered here (Q6)", () => {
  afterEach(() => {
    clearPairOutcome();
    resetDevicesForTests();
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  it("says so under the bar, not in a toast over its buttons", async () => {
    const req = { id: "pr_1", fingerprint: STUDIO.fingerprint!, name: "mac-studio", platform: "linux", state: "pending", mode: "code", created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 300_000).toISOString() };
    let reqs = [req];
    stubRouter({
      "GET /devices": [MAC],
      "GET /devices/self": SELF,
      "GET /devices/activity": [],
      "GET /devices/pair-requests": () => reqs,
      "GET /peers": [],
      "POST /devices/pair-requests/pr_1/approve": () => ((reqs = []), { ...req, state: "approved" }),
    });
    render(
      <ToastViewport>
        <PairedDevicesSection />
        <PairRequestWatcher />
      </ToastViewport>,
    );
    const add = await screen.findByRole("button", { name: "Add with control" }, { timeout: 3000 });
    await waitFor(() => expect((add as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(add);
    const line = await waitFor(() => {
      const el = document.querySelector('[data-pair-outcome="added"]');
      if (!el) throw new Error("no outcome line");
      return el as HTMLElement;
    });
    expect(line.textContent).toContain("mac-studio added. It may control mads on this pop-os.");
    expect(document.querySelector("[data-toast-viewport]")!.textContent).not.toContain("mac-studio added");
    fireEvent.click(within(line).getByRole("button", { name: "Dismiss" }));
    expect(document.querySelector("[data-pair-outcome]")).toBeNull();
  });
});
