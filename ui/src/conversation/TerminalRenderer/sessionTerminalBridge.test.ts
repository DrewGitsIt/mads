import { describe, expect, it, vi } from "vitest";
import { SessionTerminalBridge } from "./sessionTerminalBridge";
import { FakeSessionEventSource } from "../../router/fixtures";
import { FakeRouterClient } from "../../router/fixtures";

describe("SessionTerminalBridge", () => {
  it("delivers pty_output frames to onData, ignoring other event kinds", () => {
    const source = new FakeSessionEventSource();
    const bridge = new SessionTerminalBridge(new FakeRouterClient(), "session-active", source);

    const received: string[] = [];
    bridge.onData("session-active", (data) => received.push(data));

    source.emit({ kind: "text", text: "chat text — must not reach the terminal" });
    source.emit({ kind: "pty_output", text: "$ echo hi\r\n" });

    expect(received).toEqual(["$ echo hi\r\n"]);
  });

  it("unsubscribes cleanly", () => {
    const source = new FakeSessionEventSource();
    const removeSpy = vi.spyOn(source, "removeEventListener");
    const bridge = new SessionTerminalBridge(new FakeRouterClient(), "session-active", source);

    const unsubscribe = bridge.onData("session-active", () => {});
    unsubscribe();

    expect(removeSpy).toHaveBeenCalledWith("pty_output", expect.any(Function));
  });
});
