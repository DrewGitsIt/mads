import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { SCENARIOS, scenarioById } from "./scenarios";

const sets = join(dirname(fileURLToPath(import.meta.url)), "../../../tools/perf/scenarioSets.sh");
const resolve = (arg: string): string[] =>
  execFileSync("sh", ["-c", `. "$1"; resolve_scenarios "$2"`, "sh", sets, arg], { encoding: "utf8" }).trim().split(",");
const ids = (xs: string[]) => xs.map((x) => scenarioById(x).id);
const isGit = (id: string) => id.startsWith("9");

describe("resolve_scenarios", () => {
  it("empty and all are every scenario, git included (so GITFX=auto builds the fixture)", () => {
    const every = SCENARIOS.map((s) => s.id).sort();
    expect(ids(resolve("")).sort()).toEqual(every);
    expect(ids(resolve("all")).sort()).toEqual(every);
    expect(resolve("").some(isGit)).toBe(true);
  });
  it("bus is every non-git scenario; git is 9a-9h", () => {
    expect(ids(resolve("bus")).sort()).toEqual(SCENARIOS.map((s) => s.id).filter((id) => !isGit(id)).sort());
    expect(resolve("git")).toEqual(["9a", "9b", "9c", "9d", "9e", "9f", "9g", "9h"]);
  });
  it("passes ids and none through and mixes with aliases", () => {
    expect(resolve("1,3b")).toEqual(["1", "3b"]);
    expect(resolve("none")).toEqual(["none"]);
    expect(resolve("bus,9a").at(-1)).toBe("9a");
  });
});
