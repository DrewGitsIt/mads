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

  // Regression test: parse() used to reconstruct its return value from only
  // `name` and `stages`, silently dropping every other top-level key. Since
  // TemplateEditor's visual mode round-trips parse() -> serialize() on every
  // field edit, that meant any unmodeled top-level key — like the
  // template-level `defaults:` block — would vanish the instant a user
  // touched an unrelated field in the visual editor. Unknown top-level keys
  // must survive the round-trip.
  it("preserves unknown top-level keys across parse -> serialize", () => {
    const source =
      "name: deploy\n" +
      "defaults:\n" +
      "  timeout: 30\n" +
      "  model: gpt-4\n" +
      "  retry: 2\n" +
      "stages:\n" +
      "  - name: build\n" +
      "    command: make build\n";

    const { parsed, error } = parse(source);
    expect(error).toBeNull();
    expect(parsed).toEqual({
      name: "deploy",
      defaults: { timeout: 30, model: "gpt-4", retry: 2 },
      stages: [{ name: "build", command: "make build" }],
    });

    const roundTripped = serialize(parsed as ParsedTemplate);
    const reparsed = parse(roundTripped);
    expect(reparsed.error).toBeNull();
    expect(reparsed.parsed).toEqual(parsed);
  });

  it("preserves an arbitrary unrecognized top-level key across parse -> serialize", () => {
    const source = "name: deploy\nstages: []\nsomeFutureField: hello\n";

    const { parsed, error } = parse(source);
    expect(error).toBeNull();
    expect(parsed).toEqual({ name: "deploy", stages: [], someFutureField: "hello" });

    const reparsed = parse(serialize(parsed as ParsedTemplate));
    expect(reparsed.error).toBeNull();
    expect(reparsed.parsed).toEqual(parsed);
  });
});
