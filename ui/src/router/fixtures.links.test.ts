import { describe, expect, it } from "vitest";
import { FakeRouterClient } from "./fixtures";

// The fake mirrors the Data Service's merge-on-create: a directed pair
// holds one link, so a re-create returns or upgrades the existing one.
describe("FakeRouterClient.createLink", () => {
  it("returns the existing link for an identical re-create", async () => {
    const client = new FakeRouterClient();
    const first = await client.createLink({ granting_session_id: "a", subject_session_id: "b", preset: "monitor" });
    const again = await client.createLink({ granting_session_id: "a", subject_session_id: "b", preset: "monitor" });
    expect(again.id).toBe(first.id);
    expect(await client.getSessionLinks("b")).toHaveLength(1);
  });

  it("upgrades a monitor link to leader in place", async () => {
    const client = new FakeRouterClient();
    const monitor = await client.createLink({ granting_session_id: "a", subject_session_id: "b", preset: "monitor" });
    const leader = await client.createLink({ granting_session_id: "a", subject_session_id: "b", preset: "leader" });
    expect(leader.id).toBe(monitor.id);
    expect(leader.grants).toContain("orchestrate");
    expect(await client.getSessionLinks("b")).toHaveLength(1);
  });
});
