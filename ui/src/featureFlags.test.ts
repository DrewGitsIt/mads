import { afterEach, describe, expect, it, vi } from "vitest";
import { FLAGS, setFlags, subscribeFlags } from "./featureFlags";

describe("featureFlags", () => {
  afterEach(() => {
    setFlags({ multiRepo: false, linkedSessions: false, remoteRegistry: false });
  });

  it("setFlags updates the shared FLAGS object in place", () => {
    setFlags({ multiRepo: true, linkedSessions: false, remoteRegistry: false });
    expect(FLAGS.multiRepo).toBe(true);
  });

  it("subscribeFlags notifies listeners on change, and unsubscribe stops notifying", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeFlags(listener);

    setFlags({ multiRepo: true, linkedSessions: false, remoteRegistry: false });
    expect(listener).toHaveBeenCalledTimes(1);

    unsubscribe();
    setFlags({ multiRepo: false, linkedSessions: true, remoteRegistry: false });
    expect(listener).toHaveBeenCalledTimes(1);
  });
});
