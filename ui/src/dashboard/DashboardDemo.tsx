import { DashboardTab } from "./DashboardTab";
import { FakeMetricsClient } from "./client/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import styles from "./DashboardDemo.module.css";

// No real Router backend here — stub the global EventSource so
// useLiveActivity's subscribeToEvents() gets a scripted source instead of
// attempting a real SSE connection (HANDOFF.md §5/§7).
FakeEventSource.instances = [];
globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;

const client = new FakeMetricsClient();

/** Demo mount: Dashboard tab standalone against the golden fixtures (HANDOFF.md §5). */
export function DashboardDemo() {
  return (
    <div className={styles.host}>
      <DashboardTab client={client} onDrillBack={(params) => console.log("drill-back", params)} />
    </div>
  );
}
