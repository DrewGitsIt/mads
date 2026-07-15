import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TaskFiles } from "./TaskFiles";

describe("TaskFiles", () => {
  it("shows an empty state when no files have been touched", () => {
    render(<TaskFiles touchedFiles={[]} />);
    expect(screen.getByText("No files touched yet.")).toBeTruthy();
  });

  it("tallies the touched-file count and lists each path", () => {
    const touchedFiles = ["services/pipeline/engine/engine.go", "services/pipeline/engine/engine_test.go"];
    render(<TaskFiles touchedFiles={touchedFiles} />);

    expect(screen.getByText("2 files touched")).toBeTruthy();
    expect(screen.getByText("services/pipeline/engine/engine.go")).toBeTruthy();
    expect(screen.getByText("services/pipeline/engine/engine_test.go")).toBeTruthy();
  });
});
