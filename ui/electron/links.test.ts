import { describe, expect, it } from "vitest";
import { isLoopbackHost, isRunnable, resolveLinkTarget, splitCommand } from "./links";

const BASE = "/Users/u/repo/app";
const HOME = "/Users/u";
const resolve = (href: string) => resolveLinkTarget(href, BASE, HOME);

describe("resolveLinkTarget", () => {
  it("sends web URLs to the browser as web, loopback ones as localhost", () => {
    expect(resolve("https://example.com/docs")).toEqual({ kind: "web", url: "https://example.com/docs" });
    expect(resolve("http://localhost:5173/x")).toEqual({ kind: "localhost", url: "http://localhost:5173/x" });
    expect(resolve("http://127.0.0.1:8080/")).toMatchObject({ kind: "localhost" });
    expect(resolve("http://[::1]:3000/")).toMatchObject({ kind: "localhost" });
    expect(resolve("http://app.localhost/")).toMatchObject({ kind: "localhost" });
    // Not loopback just because the name starts that way.
    expect(resolve("https://localhost.example.com/")).toMatchObject({ kind: "web" });
  });

  it("resolves a relative path against the session folder, not the app's origin", () => {
    expect(resolve("src/foo.ts")).toEqual({ kind: "file", candidates: [`${BASE}/src/foo.ts`] });
    expect(resolve("./src/../README.md")).toEqual({ kind: "file", candidates: [`${BASE}/README.md`] });
  });

  it("takes absolute, ~ and file:// paths as written", () => {
    expect(resolve("/etc/hosts")).toEqual({ kind: "file", candidates: ["/etc/hosts"] });
    expect(resolve("~/notes.md")).toEqual({ kind: "file", candidates: [`${HOME}/notes.md`] });
    expect(resolve("file:///tmp/a%20b.txt")).toEqual({ kind: "file", candidates: ["/tmp/a b.txt"] });
  });

  it("decodes the percent-encoding markdown adds", () => {
    expect(resolve("docs/My%20Notes.md")).toEqual({ kind: "file", candidates: [`${BASE}/docs/My Notes.md`] });
  });

  it("tries the path as written first, then without a line or #L location", () => {
    expect(resolve("src/foo.ts:42")).toEqual({
      kind: "file",
      candidates: [`${BASE}/src/foo.ts:42`, `${BASE}/src/foo.ts`],
    });
    expect(resolve("src/foo.ts:42:7")).toMatchObject({ candidates: [`${BASE}/src/foo.ts:42:7`, `${BASE}/src/foo.ts`] });
    expect(resolve("foo.ts:42")).toMatchObject({ kind: "file", candidates: [`${BASE}/foo.ts:42`, `${BASE}/foo.ts`] });
    expect(resolve("src/foo.ts#L10-L20")).toMatchObject({ candidates: [`${BASE}/src/foo.ts#L10-L20`, `${BASE}/src/foo.ts`] });
  });

  it("opens nothing for other schemes, in-page anchors, or a relative path with no folder", () => {
    expect(resolve("mailto:a@b.c")).toEqual({ kind: "none" });
    expect(resolve("javascript:alert(1)")).toEqual({ kind: "none" });
    expect(resolve("#section")).toEqual({ kind: "none" });
    expect(resolve("")).toEqual({ kind: "none" });
    expect(resolveLinkTarget("src/foo.ts", undefined, HOME)).toEqual({ kind: "none" });
  });

  it("refuses a file:// URL on another host", () => {
    expect(resolve("file://server/share/x.txt")).toEqual({ kind: "none" });
  });
});

describe("isLoopbackHost", () => {
  it("knows the loopback names", () => {
    expect(["localhost", "127.0.0.1", "[::1]", "x.localhost"].every(isLoopbackHost)).toBe(true);
    expect(isLoopbackHost("example.com")).toBe(false);
  });
});

describe("isRunnable", () => {
  const plain = { isFile: true, mode: 0o644 };
  it("flags types whose default app runs them, per platform", () => {
    expect(isRunnable("/a/x.command", plain, "darwin")).toBe(true);
    expect(isRunnable("/a/X.WEBLOC", plain, "darwin")).toBe(true);
    expect(isRunnable("/a/x.desktop", plain, "linux")).toBe(true);
    expect(isRunnable("/a/x.AppImage", plain, "linux")).toBe(true);
    expect(isRunnable("/a/x.sh", plain, "linux")).toBe(true);
  });

  it("checks a bundle by its extension even though it is a directory", () => {
    expect(isRunnable("/Applications/Evil.app", { isFile: false, mode: 0o755 }, "darwin")).toBe(true);
  });

  it("flags any file with an execute bit", () => {
    expect(isRunnable("/a/tool", { isFile: true, mode: 0o755 }, "darwin")).toBe(true);
    expect(isRunnable("/a/tool", { isFile: true, mode: 0o744 }, "linux")).toBe(true);
  });

  it("leaves documents and ordinary folders alone", () => {
    expect(isRunnable("/a/notes.md", plain, "darwin")).toBe(false);
    expect(isRunnable("/a/src", { isFile: false, mode: 0o755 }, "linux")).toBe(false);
  });
});

describe("splitCommand", () => {
  it("splits plain words", () => {
    expect(splitCommand("code -g")).toEqual(["code", "-g"]);
    expect(splitCommand("  subl   -w ")).toEqual(["subl", "-w"]);
  });

  it("keeps quoted and escaped spaces in one argument", () => {
    expect(splitCommand(`"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code" -g`)).toEqual([
      "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code",
      "-g",
    ]);
    expect(splitCommand(`'/opt/My Editor/bin/ed' --wait`)).toEqual(["/opt/My Editor/bin/ed", "--wait"]);
    expect(splitCommand(`/opt/My\\ Editor/ed`)).toEqual(["/opt/My Editor/ed"]);
    expect(splitCommand(`ed ""`)).toEqual(["ed", ""]);
  });

  it("does no shell expansion", () => {
    expect(splitCommand("ed $HOME; rm -rf ~")).toEqual(["ed", "$HOME;", "rm", "-rf", "~"]);
  });

  it("rejects an empty or unbalanced command", () => {
    expect(splitCommand("")).toBeUndefined();
    expect(splitCommand("   ")).toBeUndefined();
    expect(splitCommand(`"code -g`)).toBeUndefined();
  });
});
