import { useEffect, useReducer, useRef, useState } from "react";
import type { SessionEventSource } from "../../router/client";
import type { ToolResult } from "../../router/types";
import { TerminalView } from "../TerminalRenderer/TerminalView";
import {
  BackgroundOutputBridge,
  BackgroundOutputSource,
  formatBytes,
} from "./backgroundOutput";
import styles from "./ActivityDock.module.css";

/** xterm history for the view: comfortably more lines than the server's 64 KB tail. */
const SCROLLBACK = 10_000;

export interface BackgroundOutputViewProps {
  sessionId: string;
  toolUseId: string;
  /** The call's own result — shown instead when there is no output to stream. */
  result: ToolResult;
  /** The process is still in the session's running list. */
  running: boolean;
  open(sessionId: string, toolUseId: string): SessionEventSource;
}

/**
 * The output of a call that kept running as a background process, streamed
 * from the harness's own file into a read-only terminal — live while it
 * runs, and afterwards for as long as the file is kept.
 */
export function BackgroundOutputView({
  sessionId,
  toolUseId,
  result,
  running,
  open,
}: BackgroundOutputViewProps) {
  const [stream, setStream] = useState<{
    source: BackgroundOutputSource;
    bridge: BackgroundOutputBridge;
  }>();
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  const openRef = useRef(open);
  openRef.current = open;
  // Made in an effect, not during render, so StrictMode's rehearsal unmount
  // closes a stream nothing else holds.
  useEffect(() => {
    const source = new BackgroundOutputSource(
      openRef.current(sessionId, toolUseId),
    );
    const off = source.onState(rerender);
    setStream({ source, bridge: new BackgroundOutputBridge(source) });
    return () => {
      off();
      source.close();
    };
  }, [sessionId, toolUseId]);

  if (!stream) return <div className={styles.lbl}>output</div>;
  const { source, bridge } = stream;
  const { endReason, trimmed, received } = source;
  const state =
    endReason === "ended"
      ? "exited"
      : endReason === undefined
        ? running
          ? "running"
          : "finished"
        : undefined;

  if (endReason === "gone") {
    return (
      <>
        <div className={styles.lbl}>output</div>
        <pre className={styles.pre} data-testid="background-output-gone">
          process output file removed
        </pre>
      </>
    );
  }
  if (endReason === "none") {
    // Nothing to stream: the call's own result is all there is.
    return (
      <>
        <div className={styles.lbl}>output</div>
        <pre className={styles.pre}>{result.text ?? ""}</pre>
      </>
    );
  }
  return (
    <>
      <div className={styles.lbl}>output{state ? ` · ${state}` : ""}</div>
      {trimmed > 0 && (
        <div className={styles.trimmed} data-testid="background-output-trimmed">
          earlier output trimmed ({formatBytes(trimmed)})
        </div>
      )}
      <div className={styles.bgTerm} data-testid="background-output">
        <TerminalView bridge={bridge} convertEol scrollback={SCROLLBACK} />
      </div>
      {endReason === "ended" && received === 0 && trimmed === 0 && (
        <div className={styles.trimmed}>no output</div>
      )}
    </>
  );
}
