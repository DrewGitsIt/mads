import { describe, expect, it } from "vitest";
import { KNOWN_STEPS, reduceSteps } from "./provisioningSteps";

describe("reduceSteps", () => {
  it("marks only the first step active before any event arrives, the rest pending", () => {
    const steps = reduceSteps([], false);
    expect(steps[0].status).toBe("active");
    expect(steps.slice(1).every((s) => s.status === "pending")).toBe(true);
  });

  it("marks the first step active once the first known step name arrives", () => {
    const steps = reduceSteps(["worktree"], false);
    expect(steps[0].status).toBe("done");
    expect(steps[1].status).toBe("active");
    expect(steps.slice(2).every((s) => s.status === "pending")).toBe(true);
  });

  it("reflects today's backend reality: a single provider_ready event marks steps 1-4 done and step 5 done too", () => {
    const steps = reduceSteps(["provider_ready"], false);
    const lastIndex = KNOWN_STEPS.length - 1;
    expect(steps.slice(0, lastIndex + 1).every((s) => s.status === "done")).toBe(true);
  });

  it("marks every step done once the session goes active, regardless of received steps", () => {
    const steps = reduceSteps([], true);
    expect(steps.every((s) => s.status === "done")).toBe(true);
  });

  it("degrades correctly for a hypothetical richer step sequence", () => {
    const steps = reduceSteps(["worktree", "hook", "agent_starting"], false);
    expect(steps[0].status).toBe("done");
    expect(steps[1].status).toBe("done");
    expect(steps[2].status).toBe("done");
    expect(steps[3].status).toBe("active");
    expect(steps[4].status).toBe("pending");
  });

  it("ignores unknown step names, treating it as no progress yet", () => {
    const steps = reduceSteps(["some_future_step"], false);
    expect(steps[0].status).toBe("active");
    expect(steps.slice(1).every((s) => s.status === "pending")).toBe(true);
  });
});
