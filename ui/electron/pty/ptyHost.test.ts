import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { resolveCwd } from "./ptyHost";

/**
 * Importing ptyHost.ts here does not pull in node-pty: the module's only
 * reference to it is a dynamic `import("node-pty")` inside loadNodePty(),
 * which only runs once a "pty:spawn" IPC handler actually fires. resolveCwd
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
