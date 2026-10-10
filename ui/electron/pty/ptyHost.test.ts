import { chmodSync, mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ensureSpawnHelperExecutable, resolveCwd } from "./ptyHost";

/**
 * Importing ptyHost.ts here does not pull in node-pty: the module's only
 * reference to it is a dynamic `import("node-pty")` inside loadNodePty(),
 * which only runs once a shell is actually opened. resolveCwd
 * is plain, synchronous, and independently testable.
 */
describe("resolveCwd", () => {
  const originalHome = process.env.HOME;

  beforeEach(() => {
    process.env.HOME = "/home/drew";
  });

  afterEach(() => {
    process.env.HOME = originalHome;
  });

  it("honors an absolute cwd", () => {
    expect(resolveCwd("/repo/mads")).toBe("/repo/mads");
  });

  it("falls back to HOME for a relative cwd (e.g. a stored session cwd of '.')", () => {
    expect(resolveCwd(".")).toBe("/home/drew");
  });

  it("falls back to HOME for any non-absolute path", () => {
    expect(resolveCwd("some/relative/dir")).toBe("/home/drew");
  });

  it("falls back to HOME when no cwd is given", () => {
    expect(resolveCwd(undefined)).toBe("/home/drew");
  });
});

describe("ensureSpawnHelperExecutable", () => {
  let ptyDir: string;
  const helper = (...parts: string[]) => join(ptyDir, ...parts, "spawn-helper");
  const place = (path: string, mode: number) => {
    mkdirSync(join(path, ".."), { recursive: true });
    writeFileSync(path, "");
    chmodSync(path, mode);
  };
  const modeOf = (path: string) => statSync(path).mode & 0o777;

  beforeEach(() => {
    ptyDir = mkdtempSync(join(tmpdir(), "node-pty-"));
  });

  afterEach(() => {
    rmSync(ptyDir, { recursive: true, force: true });
  });

  it("makes the platform's prebuilt helper executable (node-pty 1.1.0 ships it 0644)", () => {
    const prebuilt = helper("prebuilds", `${process.platform}-${process.arch}`);
    place(prebuilt, 0o644);
    ensureSpawnHelperExecutable(ptyDir);
    expect(modeOf(prebuilt)).toBe(0o755);
  });

  it("fixes a source build's helper too, and leaves an executable one alone", () => {
    const built = helper("build", "Release");
    const prebuilt = helper("prebuilds", `${process.platform}-${process.arch}`);
    place(built, 0o600);
    place(prebuilt, 0o750);
    ensureSpawnHelperExecutable(ptyDir);
    expect(modeOf(built)).toBe(0o755);
    expect(modeOf(prebuilt)).toBe(0o750);
  });

  it("does nothing when there is no helper", () => {
    expect(() => ensureSpawnHelperExecutable(ptyDir)).not.toThrow();
  });
});
