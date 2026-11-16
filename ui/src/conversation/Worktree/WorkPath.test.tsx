import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import type { Session } from "../../router/types";
import { WorkPath } from "./WorkPath";
import type { PushFacts } from "./workState";

const inPlace: Session = { ...fixtureSessions["session-active"], uncommitted_count: 0 };
/** pushFacts for one repo with `commits` to push. */
const toPush = (commits: number, target: "branch" | "base" = "branch"): PushFacts => ({
  commits,
  repos: commits > 0 ? [{ root: "/r", primary: true, target, commits }] : [],
});
const step = () => screen.getByTestId("work-step-push");

describe("WorkPath — the Push step (D5, mockup §3)", () => {
  it("in place: N to push, and Push pushes the branch", () => {
    const onPush = vi.fn();
    render(<WorkPath session={inPlace} push={toPush(2)} onPush={onPush} />);
    expect(step().getAttribute("data-state")).toBe("next");
    expect(step().textContent).toContain("2 to push");
    expect(within(step()).getByText("2")).toBeTruthy();
    fireEvent.click(within(step()).getByRole("button", { name: "Push" }));
    expect(onPush).toHaveBeenCalledWith("branch");
    expect(screen.queryByTestId("work-step-land")).toBeNull();
  });

  it("doesn't claim pushed off a 0 alone: quiet until a push went through", () => {
    const { rerender } = render(<WorkPath session={inPlace} push={toPush(0)} onPush={vi.fn()} />);
    expect(step().getAttribute("data-state")).toBe("idle");
    expect(step().textContent).toContain("nothing to push");
    expect(step().textContent).not.toContain("✓");
    expect(within(step()).queryByRole("button")).toBeNull();
    rerender(<WorkPath session={inPlace} push={toPush(0)} onPush={vi.fn()} pushed />);
    expect(step().getAttribute("data-state")).toBe("done");
    expect(step().textContent).toContain("✓");
    expect(step().textContent).toContain("pushed");
  });

  it("isolated, before Land: explains both pushes and offers the branch for a PR", () => {
    const onPush = vi.fn();
    render(<WorkPath session={{ ...fixtureIsolatedSession, uncommitted_count: 0 }} onPush={onPush} />);
    expect(step().getAttribute("data-state")).toBe("wait");
    expect(step().textContent).toContain("before Land: pushes mads/session/session-isolated (for a PR)");
    expect(step().textContent).toContain("after Land: pushes main");
    const button = within(step()).getByRole("button", { name: "Push branch" });
    expect(button.className).not.toMatch(/btnPrimary/);
    fireEvent.click(button);
    expect(onPush).toHaveBeenCalledWith("branch");
  });

  it("isolated, after Land: Push pushes the base", () => {
    const onPush = vi.fn();
    render(
      <WorkPath
        session={{ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 }}
        push={toPush(3, "base")}
        onPush={onPush}
      />,
    );
    expect(step().getAttribute("data-state")).toBe("next");
    expect(step().textContent).toContain("3 to push · pushes main");
    expect(screen.getByTestId("work-step-land").getAttribute("data-state")).toBe("done");
    fireEvent.click(within(step()).getByRole("button", { name: "Push" }));
    expect(onPush).toHaveBeenCalledWith("base");
  });

  it("offers Push, not as the next step, while there's still something to commit", () => {
    render(<WorkPath session={{ ...inPlace, uncommitted_count: 1 }} push={toPush(2)} onPush={vi.fn()} onCommit={vi.fn()} />);
    expect(step().getAttribute("data-state")).toBe("wait");
    expect(within(step()).getByRole("button", { name: "Push" }).className).not.toMatch(/btnPrimary/);
  });

  it("has no Push button without a handler", () => {
    render(<WorkPath session={inPlace} push={toPush(2)} />);
    expect(within(step()).queryByRole("button")).toBeNull();
  });

  it("says nothing about Push until the repo state has answered: no 0, no button", () => {
    render(<WorkPath session={inPlace} onPush={vi.fn()} pushed />);
    expect(step().getAttribute("data-state")).toBe("idle");
    expect(step().textContent).toContain("checking…");
    expect(step().textContent).not.toContain("✓");
    expect(within(step()).queryByRole("button")).toBeNull();
  });
});
