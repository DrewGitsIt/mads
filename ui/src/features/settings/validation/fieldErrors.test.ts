import { describe, expect, it } from "vitest";
import { fieldErrorMap } from "./fieldErrors";

describe("fieldErrorMap", () => {
  it("maps every error, keyed by field", () => {
    const map = fieldErrorMap([
      { field: "concurrency_limit", message: "must be at least 1" },
      { field: "log_retention_days", message: "must be at least 1" },
    ]);
    expect(map).toEqual({
      concurrency_limit: "must be at least 1",
      log_retention_days: "must be at least 1",
    });
  });

  it("returns an empty map for an empty error list", () => {
    expect(fieldErrorMap([])).toEqual({});
  });
});
