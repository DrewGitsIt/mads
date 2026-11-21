import { describe, expect, it } from "vitest";
import type { Envelope } from "../../../../router/types";
import {
  activityLine,
  cappedLine,
  hostWord,
  kindOf,
  letsCell,
  levelOf,
  needsConfirm,
  relTime,
  shortFingerprint,
  showSessionsWhy,
  statusCell,
} from "./model";
import { DISPLAY, fp, MAC, PHONE, row, SELF, STUDIO } from "./testRouter";

const text = (env: Envelope, rows = [MAC, PHONE, DISPLAY]) =>
  activityLine(env, rows)
    .parts.map((p) => (typeof p === "string" ? p : p.bold))
    .join("");
const env = (type: string, payload: object, actor = "router"): Envelope => ({ seq: 1, timestamp: new Date().toISOString(), type, actor, payload });

describe("paired devices model", () => {
  it("detects a device's kind from its platform, never asks", () => {
    expect(["darwin", "linux", "windows"].map(kindOf)).toEqual(["computer", "computer", "computer"]);
    expect(["ios", "android"].map(kindOf)).toEqual(["phone", "phone"]);
    expect([undefined, "", "plan9"].map(kindOf)).toEqual(["unknown", "unknown", "unknown"]);
  });

  it("names this device by its short word", () => {
    expect(hostWord(SELF)).toBe("pop-os");
    expect(hostWord({ ...SELF, name: "Drews-MacBook-Air" })).toBe("MacBook-Air");
    expect(hostWord(undefined)).toBe("device");
  });

  it("asks before raising to control or administer, never before lowering", () => {
    expect(needsConfirm("view", "control")).toBe(true);
    expect(needsConfirm("control", "admin")).toBe(true);
    expect(needsConfirm("none", "chat")).toBe(false);
    expect(needsConfirm("admin", "control")).toBe(false);
  });

  describe("Lets this … ", () => {
    it("reads — for a device that doesn't host mads", () => {
      const c = letsCell(PHONE, "pop-os");
      expect(c.text).toBe("—");
      expect(c.tip).toContain("doesn't host mads sessions");
    });
    it("reads — (Not reached yet) for a host never reached", () => {
      const c = letsCell(row({ name: "spark", fingerprint: fp("K"), mesh_url: "http://spark:1" }), "pop-os");
      expect(c).toMatchObject({ text: "—", tip: "Not reached yet." });
    });
    it("reads the host's verb, and its last known one, marked stale, while it's offline", () => {
      expect(letsCell(STUDIO, "pop-os")).toMatchObject({ text: "administer", stale: false });
      expect(letsCell(MAC, "pop-os")).toMatchObject({ text: "not use", level: "none" });
      const off = letsCell({ ...STUDIO, reachability: "unreachable" }, "pop-os");
      expect(off).toMatchObject({ text: "administer", stale: true });
      expect(off.tip).toMatch(/^Last known, as of .*offline right now\.$/);
    });
  });

  describe("Status", () => {
    it("a host says whether this device reaches it", () => {
      expect(statusCell(STUDIO, "pop-os")).toMatchObject({ text: "Connected · 9 ms", dot: "ok" });
      expect(statusCell({ ...STUDIO, reachability: "unreachable" }, "pop-os")).toMatchObject({ text: "Offline", dot: "bad" });
      const denied = statusCell(MAC, "pop-os");
      expect(denied).toMatchObject({ text: "Doesn't let this pop-os use mads", dot: "warn" });
      expect(denied.tip).toContain("That's set on Drews-MacBook-Air.");
      expect(denied.tip).toContain("It has never used this pop-os.");
    });
    it("a phone or a display says when it last used this device", () => {
      const used = statusCell(PHONE, "pop-os");
      expect(used).toMatchObject({ text: "Last used this pop-os 5 min ago", dot: "ok" });
      expect(used.tip).toContain("doesn't host mads, so this pop-os never connects to it");
      expect(statusCell(DISPLAY, "pop-os")).toMatchObject({ text: "Never used this pop-os", dot: "off" });
    });
  });

  it("Show sessions is off limits with a reason only when there's nothing to show", () => {
    expect(showSessionsWhy(PHONE, "pop-os")).toBe("Drews-iPhone doesn't host mads sessions.");
    expect(showSessionsWhy(MAC, "pop-os")).toBe("Drews-MacBook-Air lets this pop-os not use mads, so there are no sessions to show.");
    expect(showSessionsWhy(STUDIO, "pop-os")).toBe("");
    // Never reached: not known to refuse, so it may be tried.
    expect(showSessionsWhy(row({ name: "spark", fingerprint: fp("K"), mesh_url: "http://spark:1" }), "pop-os")).toBe("");
  });

  it("words the activity log", () => {
    expect(text(env("device.paired", { fingerprint: PHONE.fingerprint, name: "old-name", level: "chat", how: "code" }))).toBe(
      "Drews-iPhone paired with a pairing code; may chat with",
    );
    expect(text(env("device.grant_changed", { fingerprint: MAC.fingerprint, level: "control", previous_level: "admin" }))).toBe(
      "Drews-MacBook-Air changed from administer to control, by this device",
    );
    expect(
      text(env("device.introduced", { fingerprint: DISPLAY.fingerprint, level: "view" }, `device:Drews-MacBook-Air/${shortFingerprint(MAC.fingerprint!)}`)),
    ).toBe("kitchen-display introduced by Drews-MacBook-Air; may view");
    expect(text(env("device.refused", { fingerprint: PHONE.fingerprint, code: "full_trust_chat_refused" }))).toContain(
      "Drews-iPhone sent a message to a Full access session. Refused",
    );
    expect(text(env("device.settings_changed", { require_tailnet: false }, "user"))).toBe(
      "Only accept devices on my tailnet turned off, by this device",
    );
    expect(activityLine(env("device.refused", { code: "no_access" }), []).chip).toEqual({ tone: "bad", label: "Refused" });
  });

  it("a grant at level \"\" is none, with its limits kept", () => {
    expect(levelOf({ ...MAC, grant: { level: "", ceilings: { full_trust_chat: "refused" } } })).toBe("none");
    expect(levelOf({ ...MAC, grant: undefined })).toBe("none");
  });

  it("says when this node's tailnet policy caps what a device grants it", () => {
    const capped = { ...STUDIO, remote: { ...STUDIO.remote!, level: "control" as const, granted: "admin" as const, tailnet_capped: true } };
    expect(cappedLine(capped)).toBe("administer, capped at control by your tailnet policy");
    const cell = letsCell(capped, "pop-os");
    expect(cell.text).toBe("control (capped)");
    expect(cell.tip).toContain("mac-studio lets this pop-os administer, capped at control by your tailnet policy.");
    expect(cappedLine(STUDIO)).toBe("");
  });

  it("reads who changed a setting from its payload, not the store's actor", () => {
    const line = activityLine(
      { seq: 1, timestamp: new Date().toISOString(), type: "device.settings_changed", actor: "settings", payload: { require_tailnet: true, by: "user" } },
      [],
    );
    expect(line.parts.map((p) => (typeof p === "string" ? p : p.bold)).join("")).toBe("Only accept devices on my tailnet turned on, by this device");
  });

  it("says an entry can't be read instead of wording it from missing fields", () => {
    for (const payload of [{ invalid_payload: "{not json" }, "text", null, [1]]) {
      const l = activityLine({ seq: 9, timestamp: new Date().toISOString(), type: "device.paired", actor: "router", payload }, []);
      expect(l.chip).toEqual({ tone: "neutral", label: "Unreadable" });
      expect(l.parts.join("")).toBe("device paired (its details can't be read)");
    }
  });

  it("says times the way the log does", () => {
    const now = new Date("2026-11-19T12:00:00Z");
    expect(relTime("2026-11-19T11:59:30Z", now)).toBe("just now");
    expect(relTime("2026-11-19T11:55:00Z", now)).toBe("5 min ago");
    expect(relTime("2026-11-19T09:00:00Z", now)).toBe("3 h ago");
    expect(relTime("2026-11-17T09:00:00Z", now)).toMatch(/^Nov 1[67]$/);
  });
});
