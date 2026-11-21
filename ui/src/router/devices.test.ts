import { afterEach, describe, expect, it, vi } from "vitest";
import { decodePairingCode, devicesApi, devicesStore, DevicesError, resetDevicesForTests } from "./devices";
import { stubRouter } from "../features/settings/sections/paired/testRouter";
import { FakeEventSource } from "./testFakeEventSource";

const FP = "4TXPR55OXYNU5KWIO7RKXWACS22QEZIKBPIDOVLCLXBPL7EE2BFQ";

function code(over: Record<string, unknown> = {}, version = 1): string {
  const body = { n: "pop-os", u: "http://pop-os.ts.net:17380", f: FP, e: Math.floor(Date.now() / 1000) + 300, s: btoa("x".repeat(32)).replace(/=+$/, ""), ...over };
  const b64 = btoa(JSON.stringify(body)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `mads-pair-${version}:${b64}`;
}

describe("decodePairingCode", () => {
  it("reads the host's name, address and fingerprint, whitespace and all", () => {
    const c = code();
    const r = decodePairingCode(`  ${c.slice(0, 30)}\n  ${c.slice(30)} `);
    expect(r).toMatchObject({ ok: true, code: { host: "pop-os", meshUrl: "http://pop-os.ts.net:17380", fingerprint: FP } });
  });

  it("says what's wrong with anything else", () => {
    expect(decodePairingCode("pop-os 4TXP-R55O")).toEqual({ ok: false, error: "That isn't a mads pairing code. Copy the whole line from the other device." });
    expect(decodePairingCode(code({}, 2))).toMatchObject({ ok: false, error: expect.stringContaining("newer mads") });
    expect(decodePairingCode(code({}, 0))).toMatchObject({ ok: false, error: expect.stringContaining("older mads") });
    expect(decodePairingCode("mads-pair-1:!!!")).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
    expect(decodePairingCode(code({ f: "short" }))).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
    expect(decodePairingCode(code({ u: "https://elsewhere/x" }))).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
  });
});

describe("devices API", () => {
  afterEach(() => {
    resetDevicesForTests();
    vi.unstubAllGlobals();
  });

  it("gives every request a timeout signal", async () => {
    const calls = stubRouter({ "GET /devices": [] });
    await devicesApi.list();
    expect(calls[0].signal).toBeInstanceOf(AbortSignal);
  });

  it("throws the Router's code and message", async () => {
    stubRouter({ "POST /devices/pair": () => new Response(JSON.stringify({ code: "already_paired", error: "pop-os is already paired here" }), { status: 409 }) });
    const err = await devicesApi.join("x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(DevicesError);
    expect(err).toMatchObject({ status: 409, code: "already_paired", message: "pop-os is already paired here" });
  });

  it("keeps the rows live off the shared LOCAL bus: no stream of its own", async () => {
    let n = 0;
    const calls = stubRouter({ "GET /devices": () => [{ id: `per_${++n}`, name: "spark", created_at: "", updated_at: "" }] });
    const off = devicesStore.subscribe(() => {});
    await vi.waitFor(() => expect(devicesStore.get().value?.[0].id).toBe("per_1"));
    expect(FakeEventSource.instances).toHaveLength(1);
    const bus = FakeEventSource.instances[0];
    expect(bus.url).toContain("/events?");
    expect(bus.url).not.toContain("session_id");
    expect(bus.url).toContain("device.grant_changed");
    bus.emit("device.grant_changed", JSON.stringify({ seq: 5, timestamp: "", type: "device.grant_changed", actor: "router" }));
    await vi.waitFor(() => expect(devicesStore.get().value?.[0].id).toBe("per_2"));
    expect(calls.filter((c) => c.path === "/devices")).toHaveLength(2);
    off();
  });
});
