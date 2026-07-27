import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { CompactionStatus } from "./CompactionStatus";

describe("CompactionStatus", () => {
  it("renders nothing when no status is given", () => {
    const { container } = render(<CompactionStatus />);
    expect(container.innerHTML).toBe("");
  });

  it("shows a compacting message during phase start", () => {
    render(<CompactionStatus status={{ phase: "start" }} />);
    expect(screen.getByText("Compacting conversation…")).toBeTruthy();
  });

  it("shows a success message on a completed successful pass", () => {
    render(<CompactionStatus status={{ phase: "done", success: true }} />);
    expect(screen.getByText("Conversation compacted.")).toBeTruthy();
  });

  it("shows the failure reason on a completed failed pass", () => {
    render(
      <CompactionStatus
        status={{ phase: "done", success: false, error: "Not enough messages to compact." }}
      />,
    );
    expect(screen.getByText("Compaction failed: Not enough messages to compact.")).toBeTruthy();
  });
});
