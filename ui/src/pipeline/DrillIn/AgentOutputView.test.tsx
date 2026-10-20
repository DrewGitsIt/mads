import { describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
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

  describe("with an empty state", () => {
    function makeEndingSource() {
      const stub = makeStubSource();
      const ends = new Set<() => void>();
      stub.source.onEnd = vi.fn((listener: () => void) => {
        ends.add(listener);
        return () => ends.delete(listener);
      });
      return { ...stub, end: () => act(() => ends.forEach((l) => l())) };
    }

    it("draws it instead of the terminal once the stream ends with zero chunks", async () => {
      const { source, end } = makeEndingSource();
      const { container } = render(<AgentOutputView source={source} empty={<p>No output.</p>} />);
      expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy();
      end();
      expect(screen.getByText("No output.")).toBeTruthy();
      expect(container.querySelector('[data-testid="terminal-view"]')).toBeNull();
    });

    it("keeps the terminal when a chunk arrived before the end", async () => {
      const { source, emit, end } = makeEndingSource();
      const { container } = render(<AgentOutputView source={source} empty={<p>No output.</p>} />);
      act(() => emit("STAGE: pass\r\n"));
      end();
      expect(screen.queryByText("No output.")).toBeNull();
      expect(container.querySelector('[data-testid="terminal-view"]')).toBeTruthy();
    });
  });

  it("closes the source on unmount", async () => {
    const { source } = makeStubSource();
    const { unmount } = render(<AgentOutputView source={source} />);
    await waitFor(() => expect(source.onChunk).toHaveBeenCalledTimes(1));

    unmount();
    expect(source.close).toHaveBeenCalledTimes(1);
  });
});
