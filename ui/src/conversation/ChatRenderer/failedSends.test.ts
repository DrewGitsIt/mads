import { describe, expect, it } from "vitest";
import type { TranscriptMessage } from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";
import { collapseFailedSends, UNDELIVERED } from "./failedSends";

let n = 0;
const user = (text: string, timestamp?: string): KeyedRow => ({ key: `u${n++}`, role: "user", text, timestamp });
const fail = (text: string, timestamp?: string): KeyedRow => ({ key: `e${n++}`, role: "agent", text, is_error: true, timestamp });
const reply = (text: string): KeyedRow => ({ key: `a${n++}`, role: "agent", text });

describe("collapseFailedSends", () => {
  it("drops a failed send's error reply and carries the error on the message", () => {
    const hi = user("hi");
    const { rows, delivery } = collapseFailedSends([hi, fail("Login expired · Please run /login")]);
    expect(rows).toEqual([hi]);
    expect(delivery.get(hi.key)).toEqual({ error: "Login expired · Please run /login", attempts: [] });
  });

  it("folds failed tries into the identical resend that follows them", () => {
    const first = user("hi", "t1");
    const second = user(" hi ", "t3");
    const third = user("hi", "t5");
    const answer = reply("hello");
    const { rows, delivery } = collapseFailedSends([
      first,
      fail("Login expired", "t2"),
      second,
      fail("ECONNREFUSED", "t4"),
      third,
      answer,
    ]);
    expect(rows).toEqual([third, answer]);
    expect(delivery.get(third.key)).toEqual({
      error: undefined,
      attempts: [
        { error: "Login expired", timestamp: "t2" },
        { error: "ECONNREFUSED", timestamp: "t4" },
      ],
    });
  });

  it("folds a failed try into a resend still in flight (no reply yet)", () => {
    const retry = user("hi");
    const { rows, delivery } = collapseFailedSends([user("hi"), fail("Login expired"), retry]);
    expect(rows).toEqual([retry]);
    expect(delivery.get(retry.key)).toEqual({ error: undefined, attempts: [{ error: "Login expired" }] });
  });

  it("keeps a failed send visible when something different follows it", () => {
    const a = user("a");
    const b = user("b");
    const ok = reply("ok");
    const { rows, delivery } = collapseFailedSends([a, fail("boom"), b, ok]);
    expect(rows).toEqual([a, b, ok]);
    expect(delivery.get(a.key)?.error).toBe("boom");
    expect(delivery.has(b.key)).toBe(false);
  });

  it("treats a reply that got anywhere as delivered, even if it ended in an error", () => {
    const hi = user("hi");
    const partial = reply("Sure, starting");
    const err = fail("Your computer went to sleep mid-response");
    const { rows, delivery } = collapseFailedSends([hi, partial, err, user("hi")]);
    expect(rows.slice(0, 3)).toEqual([hi, partial, err]);
    expect(delivery.size).toBe(0);
  });

  it("never treats an unanswered message or an inject as failed", () => {
    const inject: KeyedRow = {
      key: "inj",
      role: "user",
      text: "from leader",
      attribution: { link_id: "l1", source_name: "Leader" } as TranscriptMessage["attribution"],
    };
    const pending = user("still going");
    const { rows, delivery } = collapseFailedSends([inject, fail("x"), pending]);
    expect(rows).toHaveLength(3);
    expect(delivery.size).toBe(0);
  });

  it("a send the stream reported unsaved fails in place, with the latest failure's reason", () => {
    const older = { ...user("first"), failed: true };
    const ok = reply("ok");
    const latest = { ...user("second"), failed: true };
    const { rows, delivery } = collapseFailedSends([older, ok, latest], "connection refused");
    expect(rows).toEqual([older, ok, latest]);
    expect(delivery.get(latest.key)?.error).toBe("connection refused");
    expect(delivery.get(older.key)?.error).toBe(UNDELIVERED);
  });

  it("folds an unsaved send into its retry", () => {
    const failed = { ...user("hi"), failed: true };
    const retry = user("hi");
    const { rows, delivery } = collapseFailedSends([failed, retry, reply("hello")], "boom");
    expect(rows.map((r) => r.key)).toEqual([retry.key, expect.any(String)]);
    expect(delivery.get(retry.key)?.attempts).toEqual([{ error: "boom" }]);
  });
});
