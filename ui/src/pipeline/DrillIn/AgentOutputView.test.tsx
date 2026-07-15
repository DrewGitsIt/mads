import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { AgentOutputView } from "./AgentOutputView";
import type { PipelineOutputSource } from "../client/pipelineClient";

/** In-memory PipelineOutputSource stub — no real transport (HANDOFF.md §6). */
function makeStubSource() {
  const listeners = new Set<(chunk: string) => void>();
  const source: PipelineOutputSource = {
    onChunk: vi.fn((listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    }),
    close: vi.fn(),
  };
  return { source, emit: (chunk: string) => listeners.forEach((l) => l(chunk)) };
}

describe("AgentOutputView", () => {
  it("mounts a read-only terminal and subscribes to the source's chunks", async () => {
    const { source } = makeStubSource();
    const { container } = render(<AgentOutputView source={source} />);

    await waitFor(() => expect(source.onChunk).toHaveBeenCalledTimes(1));
    expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy();
  });

  it("appends each emitted chunk without throwing", async () => {
    const { source, emit } = makeStubSource();
    render(<AgentOutputView source={source} />);
    await waitFor(() => expect(source.onChunk).toHaveBeenCalledTimes(1));

    expect(() => emit("Reading task context...\r\n")).not.toThrow();
    expect(() => emit("STAGE: pass\r\n")).not.toThrow();
  });

  it("closes the source on unmount", async () => {
    const { source } = makeStubSource();
    const { unmount } = render(<AgentOutputView source={source} />);
    await waitFor(() => expect(source.onChunk).toHaveBeenCalledTimes(1));

    unmount();
    expect(source.close).toHaveBeenCalledTimes(1);
  });
});
