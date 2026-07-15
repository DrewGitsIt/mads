import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RunHistoryTable } from "./RunHistoryTable";
import { fixturePipelineRuns } from "../client/fixtures";

describe("RunHistoryTable", () => {
  it("renders the golden run row", () => {
    render(<RunHistoryTable runs={fixturePipelineRuns} />);
    expect(screen.getByText("ses_01hq3k")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
    expect(screen.getByText("completed")).toBeTruthy();
    expect(screen.getByText("$0.42")).toBeTruthy();
    expect(screen.getByText("architect, executor")).toBeTruthy();
  });

  it("invokes onDrillBack with the row's sessionId and runSeq", () => {
    let drilled: { sessionId: string; runSeq: number } | undefined;
    render(<RunHistoryTable runs={fixturePipelineRuns} onDrillBack={(params) => (drilled = params)} />);

    fireEvent.click(screen.getByRole("button", { name: "View run" }));

    expect(drilled).toEqual({ sessionId: "ses_01hq3k", runSeq: 3 });
  });

  it("omits the drill-back action when no callback is provided", () => {
    render(<RunHistoryTable runs={fixturePipelineRuns} />);
    expect(screen.queryByRole("button", { name: "View run" })).toBeNull();
  });
});
