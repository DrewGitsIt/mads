import { useEffect, useRef, useState, type ReactNode } from "react";
import { TerminalView } from "../../conversation/TerminalRenderer/TerminalView";
import { PipelineOutputBridge } from "./pipelineOutputBridge";
import type { PipelineOutputSource } from "../client/pipelineClient";

export interface AgentOutputViewProps {
  source: PipelineOutputSource;
  /**
   * Drawn instead of the terminal once the stream ends without a single
   * chunk (a command with no output, a missing log). Until then, and
   * without it, the terminal shows as it always has.
   */
  empty?: ReactNode;
}

/**
 * Read-only agent output for one task, reusing TerminalView (HANDOFF.md §6)
 * rather than a second terminal renderer. TerminalView spawns its bridge
 * once on mount (same lifecycle as ShellDock) — callers switching tasks
 * must remount this component (e.g. `key={taskId}`), not just pass a new
 * `source` prop.
 */
export function AgentOutputView({ source, empty }: AgentOutputViewProps) {
  const bridgeRef = useRef<PipelineOutputBridge>();
  bridgeRef.current ??= new PipelineOutputBridge(source);
  const [endedEmpty, setEndedEmpty] = useState(false);

  // Counted here, not through the bridge: TerminalView subscribes only after
  // its async spawn, and a short log can be over by then.
  const watch = empty !== undefined;
  useEffect(() => {
    if (!watch || !source.onEnd) return;
    let chunks = 0;
    const offChunk = source.onChunk(() => chunks++);
    const offEnd = source.onEnd(() => setEndedEmpty(chunks === 0));
    return () => {
      offChunk();
      offEnd();
    };
  }, [source, watch]);

  if (watch && endedEmpty) return <>{empty}</>;
  return <TerminalView bridge={bridgeRef.current} convertEol />;
}
