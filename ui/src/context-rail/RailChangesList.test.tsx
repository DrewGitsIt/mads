import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { RailChangesList } from "./RailChangesList";
import type { FileChange } from "../router/types";

const CHANGES: FileChange[] = [
  {
    id: 1,
    session_id: "session-active",
    path: "services/data/internal/ingest.go",
    change: "modified",
    origin: "tool_use",
    attributed: true,
    actor: "autonomous-executor",
    timestamp: "2026-07-01T12:00:00Z",
  },
  {
    id: 2,
    session_id: "session-active",
    path: "services/data/internal/ingest_test.go",
    change: "added",
    origin: "tool_use",
    attributed: true,
    actor: "autonomous-executor",
    timestamp: "2026-07-01T12:00:00Z",
  },
];

describe("RailChangesList", () => {
  it("shows the filename first, with the directory and attribution as meta", () => {
    render(<RailChangesList changes={CHANGES} />);

    // Filename is the primary line — the full path is no longer a text node.
    expect(screen.getByText("ingest.go")).toBeTruthy();
    expect(screen.getByText("ingest_test.go")).toBeTruthy();
    expect(screen.queryByText("services/data/internal/ingest.go")).toBeNull();
    // Directory shown once per row as dimmed meta.
    expect(screen.getAllByText("services/data/internal").length).toBe(2);
    expect(screen.getAllByText("autonomous-executor").length).toBe(2);
  });

  it("keeps the full path available on hover via title", () => {
    render(<RailChangesList changes={[CHANGES[0]]} />);
    expect(screen.getByText("ingest.go").closest("button")?.title).toBe(
      "services/data/internal/ingest.go",
    );
  });

  it("badges an unattributed change distinctly", () => {
    const unattributed: FileChange = {
      id: 99,
      session_id: "session-active",
      path: "README.md",
      change: "deleted",
      origin: "observed",
      attributed: false,
      timestamp: "2026-07-01T12:00:00Z",
    };
    render(<RailChangesList changes={[unattributed]} />);

    expect(screen.getByText("README.md")).toBeTruthy();
    expect(screen.getByText("unattributed")).toBeTruthy();
  });

  it("renders an empty state when there are no changes", () => {
    render(<RailChangesList changes={[]} />);
    expect(screen.getByText("No file changes yet.")).toBeTruthy();
  });

  it("invokes onOpenFile with the clicked path", () => {
    const onOpenFile = vi.fn();
    render(<RailChangesList changes={CHANGES} onOpenFile={onOpenFile} />);

    fireEvent.click(screen.getByText("ingest.go"));
    expect(onOpenFile).toHaveBeenCalledWith("services/data/internal/ingest.go");
  });

  it("makes rows inert when no onOpenFile is supplied", () => {
    render(<RailChangesList changes={[CHANGES[0]]} />);
    expect(screen.getByText("ingest.go").closest("button")?.disabled).toBe(true);
  });
});
