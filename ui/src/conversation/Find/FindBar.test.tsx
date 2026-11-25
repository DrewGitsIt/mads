import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { FIND_DEBOUNCE_MS, FindBar, useFindBar } from "./FindBar";
import { NO_FIND, type FindRequest, type FindStatus } from "./useFind";

let request: FindRequest | undefined;
let onStatus: (status: FindStatus) => void;
let hostRenders = 0;

function Harness() {
  const find = useFindBar();
  const stream = useRef<HTMLDivElement>(null);
  request = find.request;
  onStatus = find.onStatus;
  hostRenders++;
  return (
    <>
      <button onClick={() => find.show()}>open</button>
      <div ref={stream} />
      {find.open && <FindBar {...find.bar} streamRef={stream} />}
    </>
  );
}

describe("find bar typing", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    render(<Harness />);
    fireEvent.click(screen.getByText("open"));
  });
  afterEach(() => vi.useRealTimers());

  const input = () => screen.getByLabelText("Find in conversation");
  const type = (q: string) => fireEvent.change(input(), { target: { value: q } });
  const wait = (ms: number) => act(() => void vi.advanceTimersByTime(ms));

  it("searches once typing pauses, the wait restarting on every keystroke", () => {
    type("r");
    wait(FIND_DEBOUNCE_MS - 20);
    type("ru");
    wait(FIND_DEBOUNCE_MS - 20);
    type("rub");
    expect(request?.query).toBe("");
    // No "No results" for a search that hasn't run.
    expect(screen.queryByText("No results")).toBeNull();
    wait(FIND_DEBOUNCE_MS);
    expect(request?.query).toBe("rub");
  });

  it("searches at once on Enter, landing on the first match rather than stepping", () => {
    type("rubric");
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(request).toMatchObject({ query: "rubric", step: 0 });
    // With nothing pending, Enter steps.
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(request).toMatchObject({ query: "rubric", step: 1 });
  });

  it("clears at once", () => {
    type("rubric");
    wait(FIND_DEBOUNCE_MS);
    type("");
    expect(request?.query).toBe("");
  });

  it("shows a new status without re-rendering the bar's host", () => {
    type("rubric");
    wait(FIND_DEBOUNCE_MS);
    const before = hostRenders;
    act(() => onStatus({ ...NO_FIND, count: 12, current: 2 }));
    expect(screen.getByText("3 of 12")).toBeTruthy();
    act(() => onStatus({ ...NO_FIND, count: 5000, capped: true }));
    expect(screen.getByText("More than 1,000")).toBeTruthy();
    expect(hostRenders).toBe(before);
  });
});
