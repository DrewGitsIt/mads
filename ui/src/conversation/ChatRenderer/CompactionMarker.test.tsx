import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import { compactLine } from "./compactFormat";
import { formatMessageTime, parseMessageTime } from "./messageTime";
import { buildFeedRows } from "../../pipeline/DrillIn/feedFormat";
import type { TranscriptMessage } from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";

/** Transcript messages as keyed rows, keyed by position. */
const keyed = (ms: TranscriptMessage[]): KeyedRow[] => ms.map((m, i) => ({ ...m, key: m.key ?? `r${i}` }));

const compaction: TranscriptMessage = {
  role: "compact",
  text: "This session is being continued from a previous conversation.",
  timestamp: "2026-09-30T02:59:42Z",
  compact: { trigger: "manual", pre_tokens: 258267, post_tokens: 14428, duration_ms: 131618 },
};

describe("compaction marker", () => {
  // The screenshot bug: the carried-forward summary rendered as one giant
  // user bubble. It's a system row now, the summary behind a disclosure.
  it("renders a compaction as a system row, not a user bubble", () => {
    const { container } = render(
      <ChatRenderer
        rows={keyed([
          { role: "user", text: "before", timestamp: "2026-09-30T02:50:00Z" },
          compaction,
          { role: "user", text: "after", timestamp: "2026-09-30T03:00:10Z" },
        ])}
      />,
    );
    const marker = screen.getByRole("note", { name: "Conversation compacted" });
    expect(marker.textContent).toContain("you asked · 258k → 14k tokens · 2m 12s");
    expect(screen.queryByText(/being continued/)).toBeNull();
    expect(container.querySelectorAll("[data-user-msg]")).toHaveLength(2);

    fireEvent.click(screen.getByRole("button", { name: "Show summary" }));
    expect(screen.getByText(/being continued/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Hide summary" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("names an auto-compaction, and has no disclosure without a summary", () => {
    render(
      <ChatRenderer rows={keyed([{ role: "compact", compact: { trigger: "auto", pre_tokens: 191000, post_tokens: 12000 } }])} />,
    );
    expect(screen.getByRole("note", { name: "Conversation auto-compacted" }).textContent).toContain(
      "context was full · 191k → 12k tokens",
    );
    expect(screen.queryByRole("button", { name: "Show summary" })).toBeNull();
  });

  it("is a one-line system row in the pipeline feed, not the summary", () => {
    const rows = buildFeedRows([{ role: "user", text: "stage prompt" }, compaction]);
    expect(rows[1]).toMatchObject({ kind: "prose", role: "system", text: compactLine(compaction.compact!) });
  });
});

describe("message hover timestamps", () => {
  it("stamps each message with its time, and skips unknown ones", () => {
    const { container } = render(
      <ChatRenderer
        rows={keyed([
          { role: "user", text: "stamped", timestamp: "2026-09-29T21:52:23Z" },
          { role: "agent", text: "zero time", timestamp: "0001-01-01T00:00:00Z" },
          { role: "agent", text: "no time" },
        ])}
      />,
    );
    const stamps = container.querySelectorAll("time");
    expect(stamps).toHaveLength(1);
    expect(stamps[0].getAttribute("dateTime")).toBe("2026-09-29T21:52:23.000Z");
    expect(stamps[0].closest("[data-user-msg]")).toBeTruthy();
  });

  it("formats today as a time and other days with the date", () => {
    const now = new Date(2026, 8, 29, 23, 0);
    expect(formatMessageTime(new Date(2026, 8, 29, 22, 59), now)).not.toMatch(/Sep/);
    expect(formatMessageTime(new Date(2026, 8, 28, 16, 54), now)).toMatch(/Sep 28/);
    expect(parseMessageTime("0001-01-01T00:00:00Z")).toBeUndefined();
    expect(parseMessageTime("garbage")).toBeUndefined();
  });
});
