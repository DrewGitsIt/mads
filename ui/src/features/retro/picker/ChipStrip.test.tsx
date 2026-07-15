import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChipStrip } from "./ChipStrip";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "Session",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("ChipStrip", () => {
  it("renders one chip per session id, resolved to session names", () => {
    const sessions = [
      makeSession({ id: "s1", name: "Alpha" }),
      makeSession({ id: "s2", name: "Beta" }),
    ];
    render(<ChipStrip sessionIds={["s1", "s2"]} sessions={sessions} />);
    expect(screen.getByText("Alpha")).toBeTruthy();
    expect(screen.getByText("Beta")).toBeTruthy();
  });

  it("falls back to the raw id when a session isn't in the resolved list", () => {
    render(<ChipStrip sessionIds={["unknown"]} sessions={[]} />);
    expect(screen.getByText("unknown")).toBeTruthy();
  });
});
