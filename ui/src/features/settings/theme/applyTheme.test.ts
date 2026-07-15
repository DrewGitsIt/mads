import { afterEach, describe, expect, it, vi } from "vitest";
import { applyTheme } from "./applyTheme";

describe("applyTheme", () => {
  afterEach(() => {
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.palette;
    vi.unstubAllGlobals();
  });

  it("sets data-theme/data-palette directly for an explicit theme", () => {
    applyTheme("light", "space");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(document.documentElement.dataset.palette).toBe("space");
  });

  it('resolves "system" via matchMedia', () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query.includes("dark"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }));
    applyTheme("system", "glacier");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.dataset.palette).toBe("glacier");
  });
});
