import { describe, expect, it } from "vitest";
import type { Template } from "../../router/types";
import { runStages } from "./runStages";

const template: Template = {
  id: "tpl",
  name: "default-implementation",
  version: 3,
  created_at: "",
  updated_at: "",
  source: [
    "name: default-implementation",
    "stages:",
    "  - name: architect",
    "    agent: architect",
    "    when: '!contains(labels, \"trivial\")'",
    "  - name: build",
    "    command: make build",
    "    on_fail: { loop_back: architect, max_loops: 2, then: block }",
  ].join("\n"),
};

describe("runStages", () => {
  it("reads kind, loop-back and when from the template", () => {
    expect(runStages(template)).toEqual([
      { name: "architect", kind: "agent", loopBack: undefined, maxLoops: undefined, then: undefined, when: '!contains(labels, "trivial")' },
      { name: "build", kind: "command", loopBack: "architect", maxLoops: 2, then: "block", when: undefined },
    ]);
  });

  it("appends stages the run saw that the template no longer has", () => {
    expect(runStages(template, ["architect", "lint"]).map((s) => s.name)).toEqual(["architect", "build", "lint"]);
  });

  it("falls back to the run's own stage order without a template", () => {
    expect(runStages(undefined, ["a", "b"])).toEqual([
      { name: "a", kind: "agent" },
      { name: "b", kind: "agent" },
    ]);
    expect(runStages({ ...template, source: "stages: [" }, ["x"]).map((s) => s.name)).toEqual(["x"]);
  });
});
