import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { cleanText, decodePairingCode, devicesApi, devicesStore, DevicesError, resetDevicesForTests } from "./devices";
import { stubRouter } from "../features/settings/sections/paired/testRouter";
import { FakeEventSource } from "./testFakeEventSource";

const FP = "UQDUPBGTB6PCPZYH4ISNMOWUZYWTJKFO2IV7GANMQUQO7KPOUDFA";

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
    expect(decodePairingCode("pop-os UQDU-PBGT")).toEqual({ ok: false, error: "That isn't a mads pairing code. Copy the whole line from the other device." });
    expect(decodePairingCode(code({}, 3))).toMatchObject({ ok: false, error: expect.stringContaining("newer mads") });
    expect(decodePairingCode(code({}, 0))).toMatchObject({ ok: false, error: expect.stringContaining("older mads") });
    expect(decodePairingCode("mads-pair-1:!!!")).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
    expect(decodePairingCode(code({ f: "short" }))).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
    expect(decodePairingCode(code({ u: "https://elsewhere/x" }))).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
  });
});

describe("cleanText (S5)", () => {
  it("matches pkg/paircode.CleanText on the shared vectors", () => {
    const raw = readFileSync(resolve(__dirname, "../../../pkg/paircode/testdata/cleantext.json"), "utf8");
    const vectors = JSON.parse(raw) as { name: string; in: string; out: string }[];
    expect(vectors.length).toBeGreaterThan(5);
    for (const v of vectors) expect([v.name, cleanText(v.in)]).toEqual([v.name, v.out]);
  });
  it("a code's name and labels are cleaned", () => {
    const body = { n: "pop\u202e-os", f: FP, e: Math.floor(Date.now() / 1000) + 300, s: btoa("x".repeat(32)).replace(/=+$/, ""), a: [{ v: "direct", h: "100.1.1.1:17380", l: "Tail\u200bscale" }] };
    const b64 = Buffer.from(JSON.stringify(body)).toString("base64url");
    const r = decodePairingCode(`mads-pair-2:${b64}`);
    expect(r.ok && [r.code.host, r.code.addresses[0].label]).toEqual(["pop-os", "Tailscale"]);
  });
});

describe("decodePairingCode, v2", () => {
  const v2 = (a: unknown) => code({ u: undefined, a }, 2);
  it("reads every address, its network's kind and name, in the host's order", () => {
    const r = decodePairingCode(
      v2([
        { v: "direct", h: "100.101.4.7:17380", k: "overlay", l: "Tailscale" },
        { v: "direct", h: "192.168.1.20:17380", k: "lan", l: "Home" },
        { v: "direct", h: "[fd7a:115c:a1e0::1]:17380", k: "overlay", l: "Tailscale" },
      ]),
    );
    expect(r).toMatchObject({
      ok: true,
      code: {
        version: 2,
        host: "pop-os",
        meshUrl: "http://100.101.4.7:17380",
        addresses: [
          { addr: "100.101.4.7:17380", kind: "overlay", label: "Tailscale" },
          { addr: "192.168.1.20:17380", kind: "lan", label: "Home" },
          { addr: "[fd7a:115c:a1e0::1]:17380" },
        ],
      },
    });
  });
  it("skips an adapter it doesn't know, and refuses a code with no address it can use", () => {
    const r = decodePairingCode(v2([{ v: "relay", h: "x" }, { v: "direct", h: "pop-os.ts.net:17380", l: "Tailscale" }]));
    expect(r.ok && r.code.addresses.map((a) => a.addr)).toEqual(["pop-os.ts.net:17380"]);
    expect(decodePairingCode(v2([{ v: "relay", h: "x" }]))).toMatchObject({ ok: false, error: expect.stringContaining("no address") });
    expect(decodePairingCode(v2([]))).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
    expect(decodePairingCode(v2([{ v: "direct", h: "no-port" }]))).toMatchObject({ ok: false, error: expect.stringContaining("damaged") });
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

  it("keeps an error's other fields: the host's networks and the tips key", async () => {
    stubRouter({
      "POST /devices/pair": () =>
        new Response(JSON.stringify({ code: "no_shared_network", error: "x", host_networks: ["Tailscale"], tips: "no_shared_network" }), { status: 422 }),
    });
    const err = await devicesApi.join("x").catch((e: unknown) => e);
    expect(err).toMatchObject({ code: "no_shared_network", extra: { host_networks: ["Tailscale"], tips: "no_shared_network" } });
  });

  it("states what the approval prompt showed for a found device", async () => {
    const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": {} });
    await devicesApi.approve("pr_1", "control", { shown_vouched: true });
    await devicesApi.approve("pr_1", "view");
    expect(calls.map((c) => c.body)).toEqual([{ level: "control", shown_vouched: true }, { level: "view" }]);
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
