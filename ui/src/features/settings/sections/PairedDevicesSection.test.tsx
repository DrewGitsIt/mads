import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { resetDevicesForTests, type DeviceRow } from "../../../router/devices";
import { resetMeshForTests } from "../../../router/mesh";
import { PairedDevicesSection } from "./PairedDevicesSection";
import { DISPLAY, MAC, PHONE, SELF, STUDIO, stubRouter, type Call } from "./paired/testRouter";

const ACTIVITY = [
  { seq: 3, timestamp: new Date().toISOString(), type: "device.grant_changed", actor: "router", payload: { fingerprint: MAC.fingerprint, level: "control", previous_level: "admin" } },
  { seq: 2, timestamp: new Date().toISOString(), type: "device.paired", actor: "router", payload: { fingerprint: PHONE.fingerprint, level: "chat", how: "code" } },
];

function mount(rows: DeviceRow[] = [MAC, PHONE, DISPLAY], self = SELF): Call[] {
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
  });
  render(<PairedDevicesSection />);
  return calls;
}

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
    await waitFor(() => expect(rowOf("Drews-MacBook-Air")).toBeTruthy());
    expect(screen.getByRole("columnheader", { name: "May … mads on this pop-os" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Lets this pop-os …" })).toBeTruthy();
    const mac = within(rowOf("Drews-MacBook-Air"));
    expect(mac.getByRole("button", { name: "What Drews-MacBook-Air may do with mads on this pop-os" }).textContent).toBe("control");
    expect(rowOf("Drews-MacBook-Air").querySelector("[data-lets]")!.textContent).toBe("not use");
    expect(mac.getByText("Doesn't let this pop-os use mads")).toBeTruthy();
    const phone = within(rowOf("Drews-iPhone"));
    expect(phone.getByText("Last used this pop-os 5 min ago")).toBeTruthy();
    expect(within(rowOf("kitchen-display")).getByText("Never used this pop-os")).toBeTruthy();
    // Every status and every "Lets" has its explanation.
    const tips = screen.getAllByRole("tooltip").map((t) => t.textContent);
    expect(tips).toContain("Drews-iPhone doesn't host mads sessions, so there's nothing it could let this pop-os do.");
    expect(tips.some((t) => t?.startsWith("Drews-MacBook-Air answers, but lets this pop-os not use mads there."))).toBe(true);
  });

  it("disables Show sessions with the reason, when there's nothing to show", async () => {
    mount();
    await waitFor(() => expect(rowOf("Drews-iPhone")).toBeTruthy());
    const sw = within(rowOf("Drews-iPhone")).getByRole("switch", { name: "Show mads sessions from Drews-iPhone" });
    expect((sw as HTMLButtonElement).disabled).toBe(true);
    expect(within(rowOf("Drews-iPhone")).getAllByRole("tooltip").map((t) => t.textContent)).toContain("Drews-iPhone doesn't host mads sessions.");
    expect((within(rowOf("Drews-MacBook-Air")).getByRole("switch") as HTMLButtonElement).disabled).toBe(true);
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
    await waitFor(() => expect(rowOf("Drews-MacBook-Air")).toBeTruthy());
    fireEvent.click(within(rowOf("Drews-MacBook-Air")).getByRole("button", { name: /What Drews-MacBook-Air may do/ }));
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

  it("Introduce a device… needs a device this one administers", async () => {
    mount();
    await waitFor(() => expect(rowOf("Drews-MacBook-Air")).toBeTruthy());
    const intro = screen.getByRole("button", { name: "Introduce a device…" });
    expect(intro.getAttribute("aria-disabled")).toBe("true");
    expect(screen.getByText("You can introduce devices to a device that lets this one administer mads.")).toBeTruthy();
    fireEvent.click(intro);
    expect(screen.queryByRole("dialog")).toBeNull();
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

  it("Only accept devices on my tailnet: live, tagged Less safe when off, locked by the flag", async () => {
    const calls = mount();
    await waitFor(() => expect(screen.getByRole("switch", { name: "Only accept devices on my tailnet" }).getAttribute("aria-checked")).toBe("true"));
    fireEvent.click(screen.getByText("Advanced options"));
    fireEvent.click(screen.getByRole("switch", { name: "Only accept devices on my tailnet" }));
    await waitFor(() => expect(calls.find((c) => c.path === "/devices/settings")?.body).toEqual({ require_tailnet: false }));
    await waitFor(() => expect(screen.getByText("Less safe")).toBeTruthy());
    expect(screen.getByText("Any device with a paired key can connect, from any network this one listens on.")).toBeTruthy();
  });

  it("shows the switch locked when -require-tailnet pins it", async () => {
    mount([MAC], { ...SELF, require_tailnet_locked: true });
    await waitFor(() => expect((screen.getByRole("switch", { name: "Only accept devices on my tailnet" }) as HTMLButtonElement).disabled).toBe(true));
    expect(screen.getByText(/Set by -require-tailnet/)).toBeTruthy();
  });

  it("says when nothing is paired", async () => {
    mount([]);
    await waitFor(() => expect(screen.getByText("No paired devices")).toBeTruthy());
  });

  it("this device's short fingerprint opens to the whole one, slashed zeros and all", async () => {
    mount();
    const chip = await screen.findByRole("button", { name: "Fingerprint SSSS-SSSS-SSSS-SSSS" });
    fireEvent.click(chip);
    expect(screen.getByRole("dialog", { name: "pop-os fingerprint" }).textContent).toContain("Other devices check it when you pair.");
  });
});
