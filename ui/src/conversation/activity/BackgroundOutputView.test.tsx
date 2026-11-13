import { act, render, screen } from "@testing-library/react";
import { useEffect, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { FakeSessionEventSource } from "../../router/fixtures";
import type { ToolUse } from "../../router/types";
import type { PtyBridge } from "../TerminalRenderer/ptyBridge";
import { ActivityDock } from "./ActivityDock";
import { BackgroundOutputView } from "./BackgroundOutputView";

// A stand-in terminal that prints what its bridge feeds it.
vi.mock("../TerminalRenderer/TerminalView", () => ({
  TerminalView: ({ bridge }: { bridge: PtyBridge }) => {
    const [text, setText] = useState("");
    useEffect(() => {
      let off: (() => void) | undefined;
      void bridge.spawn().then((id) => {
        off = bridge.onData(id, (d) => setText((t) => t + d));
      });
      return () => off?.();
    }, [bridge]);
    return <pre data-testid="term">{text}</pre>;
  },
}));

const b64 = (s: string) => btoa(String.fromCharCode(...new TextEncoder().encode(s)));

function mount(running = true) {
  const sources: FakeSessionEventSource[] = [];
  const open = vi.fn(() => {
    const es = new FakeSessionEventSource();
    sources.push(es);
    return es;
  });
  const view = render(
    <BackgroundOutputView
      sessionId="ses_1"
      toolUseId="toolu_1"
      result={{ text: "Command running in background with ID: b1.", background: { task_id: "b1" } }}
      running={running}
      open={open}
    />,
  );
  const es = () => sources[sources.length - 1];
  return { view, open, es };
}

describe("BackgroundOutputView", () => {
  it("streams the process's output into the terminal while it runs", async () => {
    const { open, es } = mount();
    expect(open).toHaveBeenCalledWith("ses_1", "toolu_1");
    act(() => es().emitRaw("message", b64("compiling…\n")));
    expect(await screen.findByText("compiling…", { exact: false })).toBeTruthy();
    expect(screen.getByText("output · running")).toBeTruthy();
    act(() => es().emitRaw("message", b64("done\n[exited with code 0]\n")));
    act(() => es().emitRaw("end", "ended"));
    expect(screen.getByText("output · exited")).toBeTruthy();
    expect(screen.getByTestId("term").textContent).toContain("[exited with code 0]");
  });

  it("says how much earlier output it skipped", () => {
    const { es } = mount();
    act(() => es().emitRaw("trimmed", String(1.5 * 1024 * 1024)));
    expect(screen.getByTestId("background-output-trimmed").textContent).toBe("earlier output trimmed (1.5 MB)");
  });

  it("says plainly when the output file is gone", () => {
    const { es } = mount(false);
    act(() => es().emitRaw("end", "gone"));
    expect(screen.getByTestId("background-output-gone").textContent).toBe("process output file removed");
    expect(screen.queryByTestId("term")).toBeNull();
  });

  it("falls back to the call's own result when there is nothing to stream", () => {
    const { es } = mount(false);
    act(() => es().emitRaw("end", "none"));
    expect(screen.getByText("Command running in background with ID: b1.")).toBeTruthy();
  });

  it("labels a process that left the running list without an end marker finished", () => {
    mount(false);
    expect(screen.getByText("output · finished")).toBeTruthy();
  });

  it("closes the stream when the dock closes", () => {
    const { view, es } = mount();
    view.unmount();
    expect(es().closed).toBe(true);
  });
});

describe("ActivityDock with a background call", () => {
  const tool: ToolUse = {
    id: "toolu_1",
    name: "Bash",
    kind: "execute",
    input: { command: "make check", run_in_background: true },
    result: { text: "Command running in background with ID: b1.", background: { task_id: "b1" } },
  };

  it("shows the streamed output in place of the result text", () => {
    const es = new FakeSessionEventSource();
    render(
      <ActivityDock
        view={{ type: "tool", tool }}
        sessionId="ses_1"
        client={{ getToolDiff: async () => null, streamToolOutput: () => es }}
        background={new Map([["toolu_1", { task_id: "b1", tool_use_id: "toolu_1", started_at: new Date().toISOString() }]])}
        onClose={() => {}}
      />,
    );
    expect(screen.getByTestId("background-output")).toBeTruthy();
    expect(screen.getByText("output · running")).toBeTruthy();
    // The header reads as the row does while it runs, not as a finished call.
    expect(screen.getByText(/^running · /)).toBeTruthy();
    expect(screen.queryByText("Command running in background with ID: b1.")).toBeNull();
  });

  it("shows the result text as before for an ordinary call", () => {
    const plain = { ...tool, result: { text: "ok" } };
    render(
      <ActivityDock
        view={{ type: "tool", tool: plain }}
        sessionId="ses_1"
        client={{ getToolDiff: async () => null, streamToolOutput: () => new FakeSessionEventSource() }}
        onClose={() => {}}
      />,
    );
    expect(screen.queryByTestId("background-output")).toBeNull();
    expect(screen.getByText("ok")).toBeTruthy();
  });
});
