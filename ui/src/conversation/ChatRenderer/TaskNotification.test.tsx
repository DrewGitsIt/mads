import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import { buildFeedRows } from "../../pipeline/DrillIn/feedFormat";
import type { TranscriptMessage } from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";

/** Transcript messages as keyed rows, keyed by position. */
const keyed = (ms: TranscriptMessage[]): KeyedRow[] => ms.map((m, i) => ({ ...m, key: m.key ?? `r${i}` }));

const notice: TranscriptMessage = {
  role: "notification",
  text: 'Background command "make check" completed (exit code 0)',
  timestamp: "2026-09-30T04:57:12Z",
};

// 2026-09-30: the CLI's <task-notification> line rendered as a user bubble of
// raw XML, in the human's voice, ahead of a turn the human never started.
describe("task notification", () => {
  it("renders as a system line, not a user message", () => {
    const { container } = render(
      <ChatRenderer rows={keyed([{ role: "agent", text: "Running it in the background." }, notice])} />,
    );
    const line = screen.getByRole("note");
    expect(line.textContent).toContain('Background command "make check" completed (exit code 0)');
    expect(container.querySelectorAll("[data-user-msg]")).toHaveLength(0);
  });

  it("is a system row in the pipeline feed", () => {
    const rows = buildFeedRows([{ role: "user", text: "stage prompt" }, notice]);
    expect(rows[1]).toMatchObject({ kind: "prose", role: "system", text: notice.text });
  });
});
