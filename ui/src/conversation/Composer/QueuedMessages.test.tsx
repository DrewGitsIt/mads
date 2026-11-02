import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { QueuedMessage } from "../../router/types";
import { QueuedMessages } from "./QueuedMessages";

const at = "2026-11-02T10:00:00Z";
const digest = (id: string, link = "lnk_a"): QueuedMessage => ({
  id, text: `digest ${id}`, queued_at: at,
  attribution: { link_id: link, source_name: "Worker", digest_attributed: true, kind: "digest" },
});

describe("QueuedMessages", () => {
  it("labels a linked session's message with its sender and offers no remove", () => {
    const onCancel = vi.fn();
    render(
      <QueuedMessages
        onCancel={onCancel}
        queued={[
          { id: "q_1", text: "Can you re-run the suite?", queued_at: at,
            attribution: { link_id: "lnk_l", source_name: "Pipeline Study", relation: "your leader" } },
          { id: "q_2", text: "mine", queued_at: at },
        ]}
      />,
    );
    expect(screen.getByText("⇄ from your leader · Pipeline Study")).toBeTruthy();
    expect(screen.getByText("Can you re-run the suite?")).toBeTruthy();
    const removes = screen.getAllByRole("button", { name: "Remove queued message" });
    expect(removes).toHaveLength(1);
    fireEvent.click(removes[0]);
    expect(onCancel).toHaveBeenCalledWith("q_2");
  });

  it("folds a run of one link's digests into one row, as they deliver", () => {
    render(
      <QueuedMessages
        queued={[digest("d1"), digest("d2"), digest("d3"), digest("d4", "lnk_b"), { id: "u", text: "mine", queued_at: at }, digest("d5")]}
      />,
    );
    expect(screen.getByText("3 digests from Worker")).toBeTruthy();
    expect(screen.getAllByText("Digest from Worker")).toHaveLength(2);
    expect(screen.queryByText("digest d1")).toBeNull();
    expect(screen.getByText("digest d4")).toBeTruthy();
  });
});
