import { describe, expect, it } from "vitest";
import { GENERATION_STEPS, stepStatus } from "./stepOrder";

describe("stepStatus", () => {
  it("marks every step pending when idle", () => {
    GENERATION_STEPS.forEach((_, i) => expect(stepStatus(i, "idle")).toBe("pending"));
  });

  it("marks the starting step active during starting, the rest pending", () => {
    expect(stepStatus(0, "starting")).toBe("active");
    expect(stepStatus(1, "starting")).toBe("pending");
    expect(stepStatus(2, "starting")).toBe("pending");
  });

  it("marks starting done and gathering active during gathering", () => {
    expect(stepStatus(0, "gathering")).toBe("done");
    expect(stepStatus(1, "gathering")).toBe("active");
    expect(stepStatus(2, "gathering")).toBe("pending");
  });

  it("marks starting and gathering done, generating active during generating", () => {
    expect(stepStatus(0, "generating")).toBe("done");
    expect(stepStatus(1, "generating")).toBe("done");
    expect(stepStatus(2, "generating")).toBe("active");
  });

  it("marks every step done once completed", () => {
    GENERATION_STEPS.forEach((_, i) => expect(stepStatus(i, "completed")).toBe("done"));
  });

  it("marks every step pending on failed or cancelled (no partial progress tracked)", () => {
    GENERATION_STEPS.forEach((_, i) => {
      expect(stepStatus(i, "failed")).toBe("pending");
      expect(stepStatus(i, "cancelled")).toBe("pending");
    });
  });
});
