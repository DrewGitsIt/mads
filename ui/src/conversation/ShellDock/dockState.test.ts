import { beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_DOCK_HEIGHT,
  getShellDockState,
  resetShellDockCache,
  setShellDockState,
} from "./dockState";

describe("shell dock state", () => {
  beforeEach(() => {
    window.localStorage.clear();
    resetShellDockCache();
  });

  it("defaults to closed at the default height", () => {
    expect(getShellDockState("ses_a")).toEqual({ open: false, height: DEFAULT_DOCK_HEIGHT });
  });

  it("is kept per session id", () => {
    setShellDockState("ses_a", { open: true, height: 400 });
    expect(getShellDockState("ses_a")).toEqual({ open: true, height: 400 });
    expect(getShellDockState("ses_b").open).toBe(false);
  });

  it("survives a reload: it is read back from localStorage", () => {
    setShellDockState("ses_a", { open: true, height: 410 });
    resetShellDockCache(); // what a fresh page load starts with
    expect(getShellDockState("ses_a")).toEqual({ open: true, height: 410 });
  });

  it("ignores garbage in storage", () => {
    window.localStorage.setItem("mads.shellDock.ses_a", "{nope");
    expect(getShellDockState("ses_a").open).toBe(false);
    window.localStorage.setItem("mads.shellDock.ses_b", JSON.stringify({ open: true, height: -3 }));
    expect(getShellDockState("ses_b")).toEqual({ open: true, height: DEFAULT_DOCK_HEIGHT });
  });
});
