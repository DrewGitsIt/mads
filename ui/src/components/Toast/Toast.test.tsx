import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { defaultToastDuration, Toast, ToastViewport, useDismissible } from "./Toast";

describe("Toast", () => {
  it("renders the title", () => {
    render(<Toast title="Run finished" />);
    expect(screen.getByRole("status").textContent).toContain("Run finished");
  });

  it("renders an optional description", () => {
    render(<Toast title="Run finished" description="Completed in 2m 14s" />);
    expect(screen.getByText("Completed in 2m 14s")).toBeTruthy();
  });

  it("omits the description node when not provided", () => {
    render(<Toast title="Run finished" />);
    expect(screen.queryByText("Completed", { exact: false })).toBeNull();
  });

  it("applies the tone class", () => {
    render(<Toast tone="warn" title="Run stalled" />);
    expect(screen.getByRole("status").className).toMatch(/warn/);
  });

  it("renders a dismiss button and calls onDismiss when clicked", () => {
    const onDismiss = vi.fn();
    render(<Toast title="Run finished" onDismiss={onDismiss} />);
    fireEvent.click(screen.getByLabelText("Dismiss"));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("omits the dismiss button when onDismiss is not provided", () => {
    render(<Toast title="Run finished" />);
    expect(screen.queryByLabelText("Dismiss")).toBeNull();
  });

  it("renders an action button and calls its onClick when clicked", () => {
    const onClick = vi.fn();
    render(<Toast title="Dependency removed" action={{ label: "Undo", onClick }} />);
    fireEvent.click(screen.getByText("Undo"));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("omits the action button when action is not provided", () => {
    render(<Toast title="Run finished" />);
    expect(screen.queryByText("Undo")).toBeNull();
  });
});

describe("Toast behaviour", () => {
  afterEach(() => vi.useRealTimers());

  it("an error is an alert", () => {
    render(<Toast tone="bad" title="Couldn't commit" />);
    expect(screen.getByRole("alert").textContent).toContain("Couldn't commit");
  });

  it("clears itself after its tone's default time: 12s, or 15s for an error", () => {
    vi.useFakeTimers();
    const ok = vi.fn();
    const bad = vi.fn();
    render(
      <>
        <Toast tone="ok" title="Saved" onDismiss={ok} />
        <Toast tone="bad" title="Failed" onDismiss={bad} />
      </>,
    );
    act(() => vi.advanceTimersByTime(defaultToastDuration("ok") - 1));
    expect(ok).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(ok).toHaveBeenCalledTimes(1);
    expect(bad).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(defaultToastDuration("bad") - defaultToastDuration("ok")));
    expect(bad).toHaveBeenCalledTimes(1);
  });

  it("holds its timer while hovered and resumes with the time it had left", () => {
    vi.useFakeTimers();
    const onDismiss = vi.fn();
    render(<Toast title="Saved" duration={1000} onDismiss={onDismiss} />);
    act(() => vi.advanceTimersByTime(600));
    fireEvent.mouseEnter(screen.getByRole("status"));
    act(() => vi.advanceTimersByTime(5000));
    expect(onDismiss).not.toHaveBeenCalled();
    fireEvent.mouseLeave(screen.getByRole("status"));
    act(() => vi.advanceTimersByTime(399));
    expect(onDismiss).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("gets its full time again when its message changes", () => {
    vi.useFakeTimers();
    const onDismiss = vi.fn();
    const { rerender } = render(<Toast title="First" duration={1000} onDismiss={onDismiss} />);
    act(() => vi.advanceTimersByTime(800));
    rerender(<Toast title="Second" duration={1000} onDismiss={onDismiss} />);
    act(() => vi.advanceTimersByTime(800));
    expect(onDismiss).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(200));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("a click anywhere on a toast with a target follows it and dismisses", () => {
    const open = vi.fn();
    const onDismiss = vi.fn();
    render(<Toast title="Task created" target={{ label: "Open task", onClick: open }} onDismiss={onDismiss} />);
    fireEvent.click(screen.getByText("Task created"));
    expect(open).toHaveBeenCalledTimes(1);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("a click on a toast without a target does nothing", () => {
    const onDismiss = vi.fn();
    render(<Toast tone="bad" title="Couldn't commit" onDismiss={onDismiss} />);
    fireEvent.click(screen.getByText("Couldn't commit"));
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("its action runs without following the target, then dismisses", () => {
    const open = vi.fn();
    const undo = vi.fn();
    const onDismiss = vi.fn();
    render(
      <Toast
        title="Moved"
        action={{ label: "Undo", onClick: undo }}
        target={{ label: "Open", onClick: open }}
        onDismiss={onDismiss}
      />,
    );
    fireEvent.click(screen.getByText("Undo"));
    expect(undo).toHaveBeenCalledTimes(1);
    expect(open).not.toHaveBeenCalled();
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("a disabled action shows why", () => {
    render(
      <Toast
        title="Profile switched"
        action={{ label: "Undo", onClick: () => {}, disabled: true, hint: "Save or discard your changes to undo." }}
        onDismiss={() => {}}
      />,
    );
    expect(screen.getByText("Save or discard your changes to undo.")).toBeTruthy();
  });

  it("Esc dismisses it while it has focus", () => {
    const onDismiss = vi.fn();
    render(<Toast title="Saved" onDismiss={onDismiss} />);
    fireEvent.keyDown(screen.getByLabelText("Dismiss"), { key: "Escape" });
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});

describe("ToastViewport", () => {
  function Stack({ titles }: { titles: string[] }) {
    return (
      <ToastViewport>
        {titles.map((t) => (
          <Toast key={t} title={t} onDismiss={() => {}} />
        ))}
      </ToastViewport>
    );
  }

  it("portals toasts into one stack, three at a time, the rest behind “+N more”", () => {
    const { container, rerender } = render(<Stack titles={["a", "b", "c", "d"]} />);
    const viewport = container.querySelector("[data-toast-viewport]")!;
    expect(within(viewport as HTMLElement).getAllByRole("status").map((el) => el.textContent?.[0])).toEqual(["a", "b", "c"]);
    expect(screen.getByText("+1 more")).toBeTruthy();

    rerender(<Stack titles={["b", "c", "d"]} />);
    expect(screen.getAllByRole("status")).toHaveLength(3);
    expect(screen.getByText("d")).toBeTruthy();
    expect(screen.queryByText(/more$/)).toBeNull();
  });
});

describe("useDismissible", () => {
  function Probe({ value }: { value: string | null }) {
    const [shown, dismiss] = useDismissible(value);
    return (
      <button type="button" onClick={dismiss}>
        {shown ?? "none"}
      </button>
    );
  }

  it("hides a dismissed value until a different one, or the same one after it cleared", () => {
    const { rerender } = render(<Probe value="boom" />);
    fireEvent.click(screen.getByText("boom"));
    expect(screen.getByText("none")).toBeTruthy();
    rerender(<Probe value="boom" />);
    expect(screen.getByText("none")).toBeTruthy();
    rerender(<Probe value="bang" />);
    expect(screen.getByText("bang")).toBeTruthy();
    fireEvent.click(screen.getByText("bang"));
    rerender(<Probe value={null} />);
    rerender(<Probe value="bang" />);
    expect(screen.getByText("bang")).toBeTruthy();
  });
});
