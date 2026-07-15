import { describe, expect, it } from "vitest";
import { parse, serialize } from "./templateSync";
import type { ParsedTemplate } from "../../router/types";

describe("templateSync", () => {
  it("parses valid YAML into a ParsedTemplate", () => {
    const { parsed, error } = parse(
      "name: deploy\nstages:\n  - name: build\n    command: make build\n",
    );
    expect(error).toBeNull();
    expect(parsed).toEqual({ name: "deploy", stages: [{ name: "build", command: "make build" }] });
  });

  it("returns an error, not a throw, on invalid YAML", () => {
    const { parsed, error } = parse("name: [unterminated");
    expect(parsed).toBeNull();
    expect(error).toBeTruthy();
  });

  it("round-trips parse -> serialize -> parse to the same structure", () => {
    const original: ParsedTemplate = {
      name: "deploy",
      stages: [
        { name: "build", command: "make build" },
        { name: "test", agent: "auto", on_fail: { loop_back: "build", max_loops: 2, then: "block" } },
      ],
    };
    const source = serialize(original);
    const { parsed, error } = parse(source);
    expect(error).toBeNull();
    expect(parsed).toEqual(original);
  });
});
