import { useState } from "react";
import { PipelineTab } from "./PipelineTab";
import { FakePipelineClient } from "./client/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { recordedEnvelopes, recordedSessionId, recordedTasks } from "./client/recordedRun";
import styles from "./PipelineDemo.module.css";

// No real Router backend here — stub the global EventSource so
// usePipelineRun's subscribeToEvents() gets a scripted source instead of
// attempting a real SSE connection (HANDOFF.md §7).
FakeEventSource.instances = [];
globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;

const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));

function playRecordedRun() {
  const source = FakeEventSource.instances[FakeEventSource.instances.length - 1];
  if (!source) return;
  for (const env of recordedEnvelopes) {
    source.emit(env.type, JSON.stringify(env));
  }
}

/** Demo mount: plays the recorded engine run (HANDOFF.md §5) into the board on demand. */
export function PipelineDemo() {
  const [played, setPlayed] = useState(false);

  return (
    <div className={styles.demo}>
      <div className={styles.toolbar}>
        <button
          type="button"
          disabled={played}
          onClick={() => {
            playRecordedRun();
            setPlayed(true);
          }}
        >
          {played ? "Recorded run played" : "Play recorded sequence"}
        </button>
      </div>
      <div className={styles.host}>
        <PipelineTab sessionId={recordedSessionId} pipelineClient={client} />
      </div>
    </div>
  );
}
