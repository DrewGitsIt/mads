import { useRef } from "react";
import { TerminalView } from "../../conversation/TerminalRenderer/TerminalView";
import { PipelineOutputBridge } from "./pipelineOutputBridge";
import type { PipelineOutputSource } from "../client/pipelineClient";

export interface AgentOutputViewProps {
  source: PipelineOutputSource;
}

/**
 * Read-only agent output for one task, reusing TerminalView (HANDOFF.md §6)
 * rather than a second terminal renderer. TerminalView spawns its bridge
 * once on mount (same lifecycle as ShellDock) — callers switching tasks
 * must remount this component (e.g. `key={taskId}`), not just pass a new
 * `source` prop.
 */
export function AgentOutputView({ source }: AgentOutputViewProps) {
  const bridgeRef = useRef<PipelineOutputBridge>();
  bridgeRef.current ??= new PipelineOutputBridge(source);
  return <TerminalView bridge={bridgeRef.current} convertEol />;
}
