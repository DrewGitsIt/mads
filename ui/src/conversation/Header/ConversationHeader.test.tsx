import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ConversationHeader } from "./ConversationHeader";
import { fixtureSessions } from "../../router/fixtures";

describe("ConversationHeader", () => {
  it("renders name, status chip, and workspace/branch line", () => {
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        shellOpen={false}
        onToggleShell={() => {}}
        onRestartAgent={() => {}}
      />,
    );

    expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy();
    expect(screen.getByText("active")).toBeTruthy();
    expect(screen.getByText(/mads/)).toBeTruthy();
    expect(screen.getByText(/mads\/tsk_retry-backoff/)).toBeTruthy();
  });

  it("commits an edited name via onUpdateSession", () => {
    const onUpdateSession = vi.fn();
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        shellOpen={false}
        onToggleShell={() => {}}
        onRestartAgent={() => {}}
        onUpdateSession={onUpdateSession}
      />,
    );

    fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
    const input = screen.getByDisplayValue("Add retry backoff to the ingest worker");
    fireEvent.change(input, { target: { value: "Renamed session" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onUpdateSession).toHaveBeenCalledWith({ name: "Renamed session" });
  });

  it("kebab menu exposes exactly Shell and Restart Agent", () => {
    const onToggleShell = vi.fn();
    const onRestartAgent = vi.fn();
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        shellOpen={false}
        onToggleShell={onToggleShell}
        onRestartAgent={onRestartAgent}
      />,
    );

    fireEvent.click(screen.getByLabelText("More actions"));
    expect(screen.getByText("Shell")).toBeTruthy();
    expect(screen.getByText("Restart Agent")).toBeTruthy();
    expect(screen.queryByText("Bounce")).toBeNull();

    fireEvent.click(screen.getByText("Restart Agent"));
    expect(onRestartAgent).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByLabelText("More actions"));
    fireEvent.click(screen.getByText("Shell"));
    expect(onToggleShell).toHaveBeenCalledTimes(1);
  });

  it("renders the context meter when usage numbers are supplied", () => {
    render(
      <ConversationHeader
        session={fixtureSessions["session-active"]}
        usedTokens={50000}
        contextWindow={200000}
        shellOpen={false}
        onToggleShell={() => {}}
        onRestartAgent={() => {}}
      />,
    );
    expect(screen.getByText("25%")).toBeTruthy();
  });
});
