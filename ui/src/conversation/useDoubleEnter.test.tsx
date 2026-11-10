import { fireEvent, render } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DOUBLE_ENTER_MS, useDoubleEnter } from "./useDoubleEnter";

let now = 0;
function Host({ enabled, onDouble }: { enabled: boolean; onDouble: () => void }) {
  const scope = useRef<HTMLDivElement>(null);
  useDoubleEnter(scope, enabled, onDouble);
  return (
    <div ref={scope}>
      <div data-testid="stream" tabIndex={0}>
        reply
      </div>
      <button type="button">Open</button>
      <input aria-label="find" />
      <div data-testid="session-dock">
        <textarea aria-label="composer" />
      </div>
    </div>
  );
}

function mount(enabled = true) {
  const onDouble = vi.fn();
  const utils = render(<Host enabled={enabled} onDouble={onDouble} />);
  const composer = utils.getByLabelText("composer") as HTMLTextAreaElement;
  const enter = (el: Element, ms = 100, init: KeyboardEventInit = {}) => {
    now += ms;
    fireEvent.keyDown(el, { key: "Enter", ...init });
  };
  return { ...utils, onDouble, composer, enter };
}

describe("useDoubleEnter", () => {
  beforeEach(() => {
    now = 0;
    vi.spyOn(performance, "now").mockImplementation(() => now);
  });
  afterEach(() => vi.restoreAllMocks());

  it("fires on two quick Enters in the empty composer, once", () => {
    const { onDouble, composer, enter } = mount();
    enter(composer);
    expect(onDouble).not.toHaveBeenCalled();
    enter(composer);
    expect(onDouble).toHaveBeenCalledTimes(1);
    enter(composer); // a third starts a new count
    expect(onDouble).toHaveBeenCalledTimes(1);
  });

  it("fires from the history (a focused scroller) and from nowhere in particular", () => {
    const { onDouble, getByTestId, enter } = mount();
    enter(getByTestId("stream"));
    enter(getByTestId("stream"));
    enter(document.body);
    enter(document.body);
    expect(onDouble).toHaveBeenCalledTimes(2);
  });

  it("doesn't fire when the presses are too far apart", () => {
    const { onDouble, composer, enter } = mount();
    enter(composer);
    enter(composer, DOUBLE_ENTER_MS + 1);
    expect(onDouble).not.toHaveBeenCalled();
  });

  it("an Enter that sends typed text doesn't count", () => {
    const { onDouble, composer, enter } = mount();
    composer.value = "hello";
    enter(composer); // sends
    composer.value = "";
    enter(composer);
    expect(onDouble).not.toHaveBeenCalled();
  });

  it("controls and fields keep their own Enter, and break a count", () => {
    const { onDouble, composer, getByRole, getByLabelText, enter } = mount();
    enter(getByRole("button"));
    enter(getByRole("button"));
    enter(getByLabelText("find"));
    enter(getByLabelText("find"));
    enter(composer);
    enter(getByRole("button"));
    enter(composer);
    expect(onDouble).not.toHaveBeenCalled();
  });

  it("ignores Enter with a modifier, a held-down Enter, and does nothing while not behind", () => {
    const held = mount();
    held.enter(held.composer);
    held.enter(held.composer, 100, { shiftKey: true });
    held.enter(held.composer);
    held.enter(held.composer, 100, { repeat: true });
    expect(held.onDouble).not.toHaveBeenCalled();
    held.unmount();

    const idle = mount(false);
    idle.enter(idle.composer);
    idle.enter(idle.composer);
    expect(idle.onDouble).not.toHaveBeenCalled();
  });
});
