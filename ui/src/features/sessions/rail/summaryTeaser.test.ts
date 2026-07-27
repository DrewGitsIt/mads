import { describe, expect, it } from "vitest";
import { summaryTeaser } from "./summaryTeaser";

describe("summaryTeaser", () => {
  it("returns the accomplished body, dropping the markdown heading", () => {
    const summary =
      "## What was accomplished\n\nWired retry backoff into the ingest worker.\n\n## Key decisions\n\nUse exponential backoff.";
    expect(summaryTeaser(summary)).toBe("Wired retry backoff into the ingest worker.");
  });

  it("stops at the first blank line so only the opening paragraph shows", () => {
    const summary = "## What was accomplished\n\nFirst line.\nSecond line.\n\nLater paragraph.";
    expect(summaryTeaser(summary)).toBe("First line. Second line.");
  });

  it("strips inline emphasis and code marks", () => {
    const summary = "## What was accomplished\n\nAdded `retry` with **backoff**.";
    expect(summaryTeaser(summary)).toBe("Added retry with backoff.");
  });

  it("handles a summary with no heading by returning its first paragraph", () => {
    expect(summaryTeaser("Just a plain line.")).toBe("Just a plain line.");
  });

  it("returns empty string for an empty summary", () => {
    expect(summaryTeaser("")).toBe("");
  });
});
