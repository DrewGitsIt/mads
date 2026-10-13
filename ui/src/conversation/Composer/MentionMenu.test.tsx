import { render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MentionMenu, mentionOptionId } from "./MentionMenu";
import type { MentionCandidate } from "./useMentionResolver";

const rows: MentionCandidate[] = ["a", "b", "c"].map((n) => ({
  id: n,
  label: `/${n}`,
  insertText: `/${n}`,
  kind: "command",
}));

describe("MentionMenu", () => {
  const scrollIntoView = vi.fn();
  Element.prototype.scrollIntoView = scrollIntoView;
  afterEach(() => scrollIntoView.mockClear());

  it("scrolls the active row into view as the selection moves", () => {
    const { rerender } = render(
      <MentionMenu candidates={rows} activeIndex={0} onSelect={() => {}} />,
    );
    rerender(
      <MentionMenu candidates={rows} activeIndex={2} onSelect={() => {}} />,
    );
    const last = scrollIntoView.mock.contexts.at(-1) as Element;
    expect(last.id).toBe(mentionOptionId("c"));
    expect(scrollIntoView).toHaveBeenLastCalledWith({ block: "nearest" });
  });
});
