import { describe, expect, it } from "vitest";
import type { TranscriptMessage } from "../../router/types";
import { collapseFailedSends } from "./failedSends";

const user = (text: string, timestamp?: string): TranscriptMessage => ({ role: "user", text, timestamp });
const fail = (text: string, timestamp?: string): TranscriptMessage => ({ role: "agent", text, is_error: true, timestamp });
const reply = (text: string): TranscriptMessage => ({ role: "agent", text });

describe("collapseFailedSends", () => {
  it("drops a failed send's error reply and carries the error on the message", () => {
    const hi = user("hi");
    const { messages, delivery } = collapseFailedSends([hi, fail("Login expired · Please run /login")]);
    expect(messages).toEqual([hi]);
    expect(delivery.get(hi)).toEqual({ error: "Login expired · Please run /login", attempts: [] });
  });

  it("folds failed tries into the identical resend that follows them", () => {
    const first = user("hi", "t1");
    const second = user(" hi ", "t3");
    const third = user("hi", "t5");
    const answer = reply("hello");
    const { messages, delivery } = collapseFailedSends([
      first,
      fail("Login expired", "t2"),
      second,
      fail("ECONNREFUSED", "t4"),
      third,
      answer,
    ]);
    expect(messages).toEqual([third, answer]);
    expect(delivery.get(third)).toEqual({
      error: undefined,
      attempts: [
        { error: "Login expired", timestamp: "t2" },
        { error: "ECONNREFUSED", timestamp: "t4" },
      ],
    });
  });

  it("folds a failed try into a resend still in flight (no reply yet)", () => {
    const retry = user("hi");
    const { messages, delivery } = collapseFailedSends([user("hi"), fail("Login expired"), retry]);
    expect(messages).toEqual([retry]);
    expect(delivery.get(retry)).toEqual({ error: undefined, attempts: [{ error: "Login expired" }] });
  });

  it("keeps a failed send visible when something different follows it", () => {
    const a = user("a");
    const b = user("b");
    const { messages, delivery } = collapseFailedSends([a, fail("boom"), b, reply("ok")]);
    expect(messages).toEqual([a, b, reply("ok")]);
    expect(delivery.get(a)?.error).toBe("boom");
    expect(delivery.has(b)).toBe(false);
  });

  it("treats a reply that got anywhere as delivered, even if it ended in an error", () => {
    const hi = user("hi");
    const partial = reply("Sure, starting");
    const err = fail("Your computer went to sleep mid-response");
    const { messages, delivery } = collapseFailedSends([hi, partial, err, user("hi")]);
    expect(messages.slice(0, 3)).toEqual([hi, partial, err]);
    expect(delivery.size).toBe(0);
  });

  it("never treats an unanswered message or an inject as failed", () => {
    const inject: TranscriptMessage = {
      role: "user",
      text: "from leader",
      attribution: { link_id: "l1", source_name: "Leader" } as TranscriptMessage["attribution"],
    };
    const pending = user("still going");
    const { messages, delivery } = collapseFailedSends([inject, fail("x"), pending]);
    expect(messages).toHaveLength(3);
    expect(delivery.size).toBe(0);
  });
});
