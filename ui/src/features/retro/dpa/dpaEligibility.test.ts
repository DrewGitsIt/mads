import { describe, expect, it } from "vitest";
import { acceptedDpaCount } from "./dpaEligibility";
import type { DPA } from "../../../router/types";

function makeDpa(overrides: Partial<DPA>): DPA {
  return {
    id: "dpa_1",
    problem: "p",
    evidence: "e",
    root_cause: "r",
    remediation_layer: "tool_config",
    suggested_action: "a",
    status: "accepted",
    ...overrides,
  };
}

describe("acceptedDpaCount", () => {
  it("counts accepted and edited DPAs, excludes rejected", () => {
    const dpas = [
      makeDpa({ id: "1", status: "accepted" }),
      makeDpa({ id: "2", status: "rejected" }),
      makeDpa({ id: "3", status: "edited" }),
    ];
    expect(acceptedDpaCount(dpas)).toBe(2);
  });

  it("returns 0 for an empty list", () => {
    expect(acceptedDpaCount([])).toBe(0);
  });
});
