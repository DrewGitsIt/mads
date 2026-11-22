import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  GIT_UPDATING_FAILSAFE_MS,
  getGitUpdating,
  markFromTrigger,
  markRefreshed,
  noteGitStateEvent,
  noteGitStateRead,
  onGitUpdatingFailsafe,
  triggerParts,
  useGitUpdating,
} from "./gitUpdating";
import type { Envelope } from "./types";

const SID = "s1";
const env = (type: string, payload: unknown = {}): Envelope => ({ seq: 1, type, actor: "x", session_id: SID, payload }) as Envelope;

describe("gitUpdating (docs/GIT-STATE-DESIGN.md, the updating indicator)", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("maps the design's triggers to parts, and nothing else", () => {
    expect(triggerParts(env("file.changed"))).toEqual(["changeset", "counts"]);
    expect(triggerParts(env("session.agent_state", { state: "idle" }))).toEqual(["changeset", "counts"]);
    expect(triggerParts(env("session.agent_state", { state: "working" }))).toEqual([]);
    for (const t of ["commit.created", "commit.undone", "merge.completed"]) {
      expect(triggerParts(env(t))).toEqual(["repos", "changeset", "counts"]);
    }
    expect(triggerParts(env("repo.changed"))).toEqual(["repos", "counts"]);
    expect(triggerParts(env("session.updated"))).toEqual([]);
  });

  it("marks nothing until a read says the Router serves git state, and nothing for one that doesn't", () => {
    expect(markFromTrigger(SID, env("file.changed"))).toBe(false);
    markRefreshed(SID);
    expect(getGitUpdating(SID)).toBe("");
    noteGitStateRead(SID, "changeset", false, false);
    expect(markFromTrigger(SID, env("file.changed"))).toBe(false);
    noteGitStateRead(SID, "changeset", true, false);
    expect(markFromTrigger(SID, env("file.changed"))).toBe(true);
    expect(getGitUpdating(SID)).toBe("changeset counts");
  });

  it("clears a part on a newer generation covering it (changed or not), on a GET saying not updating, or after the fail-safe", () => {
    noteGitStateRead(SID, "repos", true, false);
    noteGitStateEvent({ session_id: SID, parts: ["counts"], generation: 10, changed: [] });
    markRefreshed(SID);
    expect(getGitUpdating(SID)).toBe("repos counts");
    noteGitStateEvent({ session_id: SID, parts: ["counts"], generation: 10, changed: [] });
    expect(getGitUpdating(SID)).toBe("repos counts");
    noteGitStateEvent({ session_id: SID, parts: ["counts"], generation: 11, changed: [] });
    expect(getGitUpdating(SID)).toBe("repos");
    noteGitStateRead(SID, "repos", true, false);
    expect(getGitUpdating(SID)).toBe("");

    noteGitStateRead(SID, "changeset", true, true);
    expect(getGitUpdating(SID)).toBe("changeset");
    vi.advanceTimersByTime(GIT_UPDATING_FAILSAFE_MS - 1);
    expect(getGitUpdating(SID)).toBe("changeset");
    vi.advanceTimersByTime(1);
    expect(getGitUpdating(SID)).toBe("");
  });

  it("re-renders a reader as marks come and go", () => {
    noteGitStateRead(SID, "repos", true, false);
    const { result } = renderHook(() => useGitUpdating(SID));
    expect(result.current).toBe("");
    act(() => void markFromTrigger(SID, env("commit.created")));
    expect(result.current).toBe("repos changeset counts");
    act(() => noteGitStateEvent({ session_id: SID, parts: ["repos", "changeset", "counts"], generation: 99, changed: ["repos"] }));
    expect(result.current).toBe("");
  });

  it("marks another baseline's change set on its own: its announcement doesn't clear session start's, nor start's it", () => {
    noteGitStateRead(SID, "changeset", true, true);
    noteGitStateRead(SID, "changeset", true, true, "base");
    expect(getGitUpdating(SID)).toBe("changeset");
    noteGitStateEvent({ session_id: SID, parts: ["changeset"], generation: 10, changed: [], baseline: "base" });
    expect(getGitUpdating(SID)).toBe("changeset"); // start's mark stands
    noteGitStateRead(SID, "changeset", true, true, "base");
    noteGitStateEvent({ session_id: SID, parts: ["changeset"], generation: 11, changed: [] });
    expect(getGitUpdating(SID)).toBe("changeset"); // base's mark stands
    noteGitStateEvent({ session_id: SID, parts: ["changeset"], generation: 12, changed: [], baseline: "base" });
    expect(getGitUpdating(SID)).toBe("");
  });

  it("re-reads a part when its fail-safe fires, and only that part's readers", () => {
    noteGitStateRead(SID, "repos", true, false);
    const repos = vi.fn();
    const start = vi.fn();
    const base = vi.fn();
    const offs = [
      onGitUpdatingFailsafe(SID, "repos", repos),
      onGitUpdatingFailsafe(SID, "changeset", start),
      onGitUpdatingFailsafe(SID, "changeset", base, "base"),
    ];
    markFromTrigger(SID, env("file.changed")); // changeset (start) and counts
    vi.advanceTimersByTime(GIT_UPDATING_FAILSAFE_MS);
    expect(getGitUpdating(SID)).toBe("");
    expect(start).toHaveBeenCalledTimes(1);
    expect(repos).not.toHaveBeenCalled();
    expect(base).not.toHaveBeenCalled();
    for (const off of offs) off();
    markFromTrigger(SID, env("file.changed"));
    vi.advanceTimersByTime(GIT_UPDATING_FAILSAFE_MS);
    expect(start).toHaveBeenCalledTimes(1);
  });
});
