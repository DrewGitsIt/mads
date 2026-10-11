import { describe, expect, it } from "vitest";
import { classifyNavigation } from "./externalLinks";

const APP = "http://localhost:17373";

describe("classifyNavigation", () => {
  it("keeps the app's own origin in the window", () => {
    expect(classifyNavigation("http://localhost:17373/conversation.html?s=1", APP)).toBe("allow");
  });

  it("sends other http(s) origins to the browser, other localhost ports included", () => {
    expect(classifyNavigation("https://example.com/docs", APP)).toBe("external");
    expect(classifyNavigation("http://localhost:3000/", APP)).toBe("external");
  });

  it("drops everything that isn't http(s)", () => {
    expect(classifyNavigation("mailto:a@b.c", APP)).toBe("block");
    expect(classifyNavigation("file:///etc/hosts", APP)).toBe("block");
    expect(classifyNavigation("javascript:alert(1)", APP)).toBe("block");
    expect(classifyNavigation("not a url", APP)).toBe("block");
  });
});
