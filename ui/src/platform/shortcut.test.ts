import { afterEach, describe, expect, it, vi } from "vitest";
import { formatShortcut, setPlatform } from "./shortcut";

afterEach(() => {
  setPlatform(undefined);
  vi.unstubAllGlobals();
});

describe("formatShortcut", () => {
  it("renders ⌘ glyphs on darwin", () => {
    expect(formatShortcut("Mod+K", "darwin")).toBe("⌘K");
    expect(formatShortcut("Mod+Shift+P", "darwin")).toBe("⌘⇧P");
    expect(formatShortcut("Mod+Enter", "darwin")).toBe("⌘Enter");
  });

  it("renders Ctrl+ on linux", () => {
    expect(formatShortcut("Mod+K", "linux")).toBe("Ctrl+K");
    expect(formatShortcut("Mod+Shift+P", "linux")).toBe("Ctrl+Shift+P");
    expect(formatShortcut("Mod+Enter", "linux")).toBe("Ctrl+Enter");
  });

  it("renders Ctrl+ for an unknown platform", () => {
    expect(formatShortcut("Mod+E", "plan9")).toBe("Ctrl+E");
  });

  it("uses the platform stored by setPlatform", () => {
    setPlatform("darwin");
    expect(formatShortcut("Mod+E")).toBe("⌘E");
    setPlatform("linux");
    expect(formatShortcut("Mod+E")).toBe("Ctrl+E");
  });

  it("falls back to navigator.platform before the Router platform loads", () => {
    vi.stubGlobal("navigator", { platform: "MacIntel" });
    expect(formatShortcut("Mod+K")).toBe("⌘K");
    vi.stubGlobal("navigator", { platform: "Linux x86_64" });
    expect(formatShortcut("Mod+K")).toBe("Ctrl+K");
  });
});
