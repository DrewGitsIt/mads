import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { resetDevicesForTests, type RemoveResult } from "../../../../router/devices";
import { IntroduceDialog } from "./IntroduceDialog";
import { RemoveDeviceDialog } from "./RemoveDeviceDialog";
import { DISPLAY, fp, MAC, PHONE, row, shortOf, STUDIO, stubRouter, type Call } from "./testRouter";

const POP = row({ name: "pop-os", fingerprint: fp("O"), mesh_url: "http://pop:1", remote: { platform: "linux", hosts_sessions: true, level: "admin", as_of: new Date().toISOString() }, reachability: "unreachable" });

const NOW = new Date().toISOString();

afterEach(() => {
  resetDevicesForTests();
  vi.unstubAllGlobals();
});

describe("RemoveDeviceDialog", () => {
  it("lists where it's removed: here, every host this device administers, and the ones it can't manage", () => {
    stubRouter({});
    render(<RemoveDeviceDialog row={PHONE} rows={[MAC, PHONE, STUDIO, POP]} selfName="Drews-MacBook-Air" host="MacBook-Air" onClose={() => {}} />);
    const dialog = screen.getByRole("alertdialog", { name: "Remove Drews-iPhone?" });
    expect(dialog.textContent).toContain("Drews-iPhone is removed from this MacBook-Air now: it can't use mads here from its next request.");
    const items = within(dialog).getAllByRole("listitem").map((li) => li.textContent);
    expect(items[0]).toBe("Drews-MacBook-Air (this device)Here");
    expect(items).toContain("mac-studioYou administer it");
    expect(items).toContain("pop-osYou administer it");
    expect(items.some((t) => t?.startsWith("Drews-MacBook-AirNot yours to manage"))).toBe(true);
    expect(dialog.textContent).toContain("Devices this MacBook-Air has never seen can't be checked.");
  });

  it("reports each host's result, with Retry where it may help", async () => {
    let retried: Call | undefined;
    const first: RemoveResult = {
      device: { name: "Drews-iPhone", fingerprint: PHONE.fingerprint! },
      here: "removed",
      hosts: [
        { name: "mac-studio", fingerprint: STUDIO.fingerprint!, result: "removed" },
        { name: "pop-os", fingerprint: POP.fingerprint!, result: "offline" },
        { name: "spark", fingerprint: fp("K"), result: "failed", error: "its Data Service is down" },
        { name: "lab", fingerprint: fp("L"), result: "not_admin" },
      ],
      cannot_manage: [{ name: "Drews-MacBook-Air", fingerprint: MAC.fingerprint!, level: "" }],
    };
    stubRouter({
      [`POST /devices/${PHONE.fingerprint}/remove-everywhere`]: (c: Call) => {
        if (!(c.body as { hosts?: string[] }).hosts) return first;
        retried = c;
        return { ...first, here: "absent", hosts: [{ name: "pop-os", fingerprint: POP.fingerprint!, result: "removed" }], cannot_manage: [] };
      },
      "GET /devices": [],
    });
    render(<RemoveDeviceDialog row={PHONE} rows={[MAC, PHONE, STUDIO, POP]} selfName="Drews-MacBook-Air" host="MacBook-Air" onClose={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
    const results = await screen.findByRole("list", { name: "Results by device" });
    const item = (name: string) => results.querySelector(`[data-remove-host="${name}"]`) as HTMLElement;
    expect(item("mac-studio").textContent).toContain("Removed");
    expect(item("pop-os").textContent).toContain("Not reached. Retry when pop-os is back.");
    expect(item("spark").textContent).toContain("answered with an error and still has it paired: its Data Service is down");
    expect(item("lab").textContent).toContain("no longer lets this MacBook-Air administer it");
    expect(within(item("mac-studio")).queryByRole("button", { name: "Retry" })).toBeNull();
    expect(results.textContent).toContain("Not yours to manage");
    fireEvent.click(within(item("pop-os")).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(item("pop-os").getAttribute("data-result")).toBe("removed"));
    expect(retried?.body).toEqual({ hosts: [POP.fingerprint], also_introduced: true });
  });

  it("offers to remove what it introduced (on by default), and reports skipped hosts", async () => {
    const intro = row({ name: "kitchen-display", fingerprint: DISPLAY.fingerprint, pairing: { paired_at: NOW, how: "introduction", paired_by: `device:Drews-MacBook-Air/${shortOf(MAC.fingerprint!)}` } });
    const out: RemoveResult = {
      device: { name: MAC.name, fingerprint: MAC.fingerprint! },
      here: "removed",
      hosts: [
        { name: "mac-studio", fingerprint: STUDIO.fingerprint!, result: "removed", also_removed: 2 },
        { name: "phone", fingerprint: fp("P"), result: "skipped", error: "it doesn't host mads, so it has no devices to remove" },
      ],
      cannot_manage: [],
      introduced_by_this: [{ name: "kitchen-display", fingerprint: DISPLAY.fingerprint! }],
      also_removed: [{ name: "kitchen-display", fingerprint: DISPLAY.fingerprint! }],
    };
    const calls = stubRouter({ [`POST /devices/${MAC.fingerprint}/remove-everywhere`]: out, "GET /devices": [] });
    render(<RemoveDeviceDialog row={MAC} rows={[MAC, intro, STUDIO]} selfName="pop-os" host="pop-os" onClose={() => {}} />);
    const box = screen.getByRole("checkbox", { name: /Also remove the device it introduced here: kitchen-display.*and any it introduced on other devices you manage/ }) as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
    const results = await screen.findByRole("list", { name: "Results by device" });
    expect((calls.find((c: Call) => c.method === "POST")!.body as object)).toEqual({ also_introduced: true });
    expect(results.textContent).toContain("With the devices it introduced: kitchen-display.");
    expect(results.querySelector('[data-remove-host="mac-studio"]')!.textContent).toContain("With 2 devices it introduced there.");
    expect(results.querySelector('[data-remove-host="phone"]')!.textContent).toContain("Skipped");
    expect(results.querySelector('[data-remove-host="phone"]')!.textContent).toContain("it doesn't host mads");
  });

  it("unchecked, the introduced devices stay", async () => {
    const intro = row({ name: "kitchen-display", fingerprint: DISPLAY.fingerprint, pairing: { paired_at: NOW, how: "introduction", paired_by: `device:Drews-MacBook-Air/${shortOf(MAC.fingerprint!)}` } });
    const calls = stubRouter({ [`POST /devices/${MAC.fingerprint}/remove-everywhere`]: { device: { name: "", fingerprint: "" }, here: "removed", hosts: [], cannot_manage: [] }, "GET /devices": [] });
    render(<RemoveDeviceDialog row={MAC} rows={[MAC, intro]} selfName="pop-os" host="pop-os" onClose={() => {}} />);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
    await screen.findByRole("list", { name: "Results by device" });
    expect(calls.find((c: Call) => c.method === "POST")!.body).toEqual({});
  });

  it("names the devices it had introduced on each host", async () => {
    const out: RemoveResult = {
      device: { name: MAC.name, fingerprint: MAC.fingerprint! },
      here: "removed",
      hosts: [
        { name: "mac-studio", fingerprint: STUDIO.fingerprint!, result: "removed", introduced_by_this: [{ name: "kitchen-display", fingerprint: DISPLAY.fingerprint! }] },
        { name: "pop-os", fingerprint: fp("O"), result: "removed", introduced_by_this: [{ name: "tv", fingerprint: fp("V") }, { name: "pad", fingerprint: fp("Q") }], also_removed: 2 },
      ],
      cannot_manage: [],
    };
    stubRouter({ [`POST /devices/${MAC.fingerprint}/remove-everywhere`]: out, "GET /devices": [] });
    render(<RemoveDeviceDialog row={MAC} rows={[MAC, STUDIO]} selfName="x" host="x" onClose={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
    const results = await screen.findByRole("list", { name: "Results by device" });
    expect(results.querySelector('[data-remove-host="mac-studio"] [data-introduced-there]')!.textContent).toBe(
      "It had introduced kitchen-display there, which stays.",
    );
    expect(results.querySelector('[data-remove-host="pop-os"] [data-introduced-there]')!.textContent).toBe(
      "It had introduced tv, pad there; all removed with it.",
    );
  });

  describe("also removing what it introduced, everywhere", () => {
    it("offers to remove what it introduced on hosts this one manages, even with none here, and sends it everywhere", async () => {
      const calls = stubRouter({
        [`POST /devices/${MAC.fingerprint}/remove-everywhere`]: { device: { name: "", fingerprint: "" }, here: "removed", hosts: [], cannot_manage: [] },
        "GET /devices": [],
      });
      render(<RemoveDeviceDialog row={MAC} rows={[MAC, STUDIO]} selfName="x" host="x" onClose={() => {}} />);
      const box = screen.getByRole("checkbox", { name: /Also remove any devices it introduced on other devices you manage/ }) as HTMLInputElement;
      expect(box.checked).toBe(true);
      expect(document.body.textContent).toContain("Here and on other devices this one administers.");
      fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
      await screen.findByRole("list", { name: "Results by device" });
      expect(calls.find((c) => c.method === "POST")!.body).toEqual({ also_introduced: true });
    });

    it("no hosts it manages and nothing introduced here: no option", () => {
      stubRouter({});
      render(<RemoveDeviceDialog row={MAC} rows={[MAC]} selfName="x" host="x" onClose={() => {}} />);
      expect(screen.queryByRole("checkbox")).toBeNull();
    });

    it("introductions that stayed on a host can be removed from the results, for that host", async () => {
      let n = 0;
      const calls = stubRouter({
        [`POST /devices/${MAC.fingerprint}/remove-everywhere`]: () =>
          ++n === 1
            ? { device: { name: "", fingerprint: "" }, here: "removed", cannot_manage: [], hosts: [{ name: "mac-studio", fingerprint: STUDIO.fingerprint!, result: "removed", introduced_by_this: [{ name: "tv", fingerprint: fp("V") }] }] }
            : { device: { name: "", fingerprint: "" }, here: "absent", cannot_manage: [], hosts: [{ name: "mac-studio", fingerprint: STUDIO.fingerprint!, result: "removed", introduced_by_this: [{ name: "tv", fingerprint: fp("V") }], also_removed: 1 }] },
        "GET /devices": [],
      });
      render(<RemoveDeviceDialog row={MAC} rows={[MAC, STUDIO]} selfName="x" host="x" onClose={() => {}} />);
      fireEvent.click(screen.getByRole("checkbox"));
      fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
      const results = await screen.findByRole("list", { name: "Results by device" });
      const studio = () => results.querySelector('[data-remove-host="mac-studio"]') as HTMLElement;
      expect(studio().textContent).toContain("It had introduced tv there, which stays.");
      fireEvent.click(within(studio()).getByRole("button", { name: "Remove them too" }));
      await waitFor(() => expect(studio().textContent).toContain("It had introduced tv there; removed with it."));
      expect(within(studio()).queryByRole("button", { name: "Remove them too" })).toBeNull();
      expect(calls.filter((c) => c.method === "POST").map((c) => c.body)).toEqual([{}, { hosts: [STUDIO.fingerprint], also_introduced: true }]);
    });
  });
});

describe("IntroduceDialog", () => {
  it("explains what's needed when this device administers nothing", () => {
    stubRouter({});
    render(<IntroduceDialog rows={[MAC, PHONE]} host="pop-os" onDone={() => {}} onCancel={() => {}} />);
    expect(screen.getByRole("dialog", { name: "Introduce a device" }).textContent).toContain("needs to administer mads on that device first");
  });

  it("introduces a device at most at control, to a host this one administers", async () => {
    const calls = stubRouter({ [`POST /devices/${PHONE.fingerprint}/introduce`]: { host: "mac-studio", device: "Drews-iPhone", level: "chat", as: "Drews-iPhone" } });
    const onDone = vi.fn();
    render(<IntroduceDialog rows={[PHONE, STUDIO]} host="pop-os" onDone={onDone} onCancel={() => {}} />);
    // A phone's suggestion: chat with.
    expect(screen.getByRole("radio", { name: "chat with" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.queryByRole("radio", { name: "administer" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Introduce" }));
    await waitFor(() => expect(onDone).toHaveBeenCalledWith({ device: "Drews-iPhone", to: "mac-studio", level: "chat" }));
    expect(calls[0].body).toEqual({ host: STUDIO.fingerprint, level: "chat" });
  });
});
