import { describe, expect, it } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ToolCard } from "./ToolCard";

describe("ToolCard", () => {
  const tool = {
    name: "Edit",
    path: "services/data/internal/ingest.go",
    input: { old_string: "foo", new_string: "bar" },
  };

  it("defaults collapsed — input not shown", () => {
    render(<ToolCard tool={tool} />);
    expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/old_string/)).toBeNull();
  });

  it("expands to show input JSON and path on click", () => {
    render(<ToolCard tool={tool} />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(/old_string/)).toBeTruthy();
    expect(screen.getAllByText(tool.path).length).toBeGreaterThan(0);
  });

  it("collapses again on a second click", () => {
    render(<ToolCard tool={tool} />);
    const button = screen.getByRole("button");
    fireEvent.click(button);
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/old_string/)).toBeNull();
  });
});

describe("ToolCard — result", () => {
  const okTool = {
    name: "Bash",
    input: { command: "go test ./..." },
    result: { text: "ok", timestamp: "2026-07-19T10:00:09Z" },
  };
  const errorTool = {
    name: "Read",
    path: "docs/screenshot.png",
    result: { text: "permission denied", is_error: true, timestamp: "2026-07-19T10:00:02Z" },
  };

  it("shows an ok status hint and duration in the collapsed row when a result is present", () => {
    render(<ToolCard tool={okTool} timestamp="2026-07-19T10:00:04Z" />);
    expect(screen.getByText("ok")).toBeTruthy();
    expect(screen.getByText("5.0s")).toBeTruthy();
  });

  it("shows an error status hint, visually distinct, for a failed result", () => {
    render(<ToolCard tool={errorTool} timestamp="2026-07-19T10:00:01Z" />);
    expect(screen.getByText("error")).toBeTruthy();
  });

  it("shows no status hint when there's no result yet", () => {
    render(<ToolCard tool={{ name: "Bash", input: { command: "sleep 5" } }} />);
    expect(screen.queryByText("ok")).toBeNull();
    expect(screen.queryByText("error")).toBeNull();
  });

  it("expanded card shows both input and result text", () => {
    render(<ToolCard tool={okTool} />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getAllByText(/go test/).length).toBeGreaterThan(0);
    expect(screen.getByText("result")).toBeTruthy();
    expect(screen.getAllByText("ok").length).toBeGreaterThan(0);
  });

  it("caps a huge result and expands it on click", () => {
    const huge = "x".repeat(3000);
    const tool = { name: "Bash", result: { text: huge } };
    render(<ToolCard tool={tool} defaultExpanded />);
    expect(screen.getByText("Show more")).toBeTruthy();
    expect(screen.getByText(/x+…/).textContent!.length).toBeLessThan(huge.length);

    fireEvent.click(screen.getByText("Show more"));
    expect(screen.getByText("Show less")).toBeTruthy();
    expect(screen.getByText(huge)).toBeTruthy();
  });
});
