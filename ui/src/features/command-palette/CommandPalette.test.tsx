import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CommandPalette } from "./CommandPalette";
import { buildCommands } from "./commandSource";
import { fixtureSessions, fixtureTasks } from "../../router/fixtures";

const sessions = Object.values(fixtureSessions);
const commands = buildCommands(sessions, fixtureTasks);

function renderPalette(overrides: Partial<Parameters<typeof CommandPalette>[0]> = {}) {
  const onClose = vi.fn();
  const onDispatch = vi.fn();
  const utils = render(
    <CommandPalette open commands={commands} onClose={onClose} onDispatch={onDispatch} {...overrides} />,
  );
  return { ...utils, onClose, onDispatch };
}

describe("CommandPalette", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders nothing when closed", () => {
    const { container } = render(
      <CommandPalette open={false} commands={commands} onClose={vi.fn()} onDispatch={vi.fn()} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it("opens over cached data without issuing a new fetch", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    renderPalette();

    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("filters results as the query changes", () => {
    renderPalette();
    const input = screen.getByPlaceholderText(/jump to a session/i);

    fireEvent.change(input, { target: { value: "new session" } });

    expect(screen.getByText("New session")).toBeTruthy();
    expect(screen.queryByText("Jump to · Add retry backoff to the ingest worker")).toBeNull();
  });

  it("shows a no-matches state for a query with no results", () => {
    renderPalette();
    const input = screen.getByPlaceholderText(/jump to a session/i);

    fireEvent.change(input, { target: { value: "zzz-nonexistent" } });

    expect(screen.getByText("No matches")).toBeTruthy();
  });

  it("clicking a jump-session row dispatches that command and closes", () => {
    const { onDispatch, onClose } = renderPalette();

    fireEvent.click(screen.getByTestId("jump:session-inactive"));

    expect(onDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "jump-session", sessionId: "session-inactive" }),
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("clicking a start-pipeline row dispatches that command with the session id", () => {
    const { onDispatch } = renderPalette();

    fireEvent.click(screen.getByTestId("start-pipeline:session-inactive"));

    expect(onDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "start-pipeline", sessionId: "session-inactive" }),
    );
  });

  it("ArrowDown moves selection to the next row before Enter dispatches it", () => {
    const { onDispatch } = renderPalette();
    const input = screen.getByPlaceholderText(/jump to a session/i);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "start-pipeline", sessionId: "session-active" }),
    );
  });

  it("ArrowUp clamps at the first row rather than wrapping", () => {
    const { onDispatch } = renderPalette();
    const input = screen.getByPlaceholderText(/jump to a session/i);

    fireEvent.keyDown(input, { key: "ArrowUp" });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "jump-session", sessionId: "session-active" }),
    );
  });

  it("calls onClose on Escape", () => {
    const { onClose } = renderPalette();

    fireEvent.keyDown(document, { key: "Escape" });

    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when clicking the scrim", () => {
    const { container, onClose } = renderPalette();

    fireEvent.click(container.firstChild as Element);

    expect(onClose).toHaveBeenCalled();
  });

  it("does not call onClose when clicking inside the panel", () => {
    const { onClose } = renderPalette();

    fireEvent.click(screen.getByPlaceholderText(/jump to a session/i));

    expect(onClose).not.toHaveBeenCalled();
  });
});
