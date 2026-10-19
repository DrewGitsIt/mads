import { TasksTab } from "./TasksTab";
import type { RunBarView } from "./RunBar/RunBar";
import {
  FakePipelineClient,
  pickedUpRunEnvelopes,
  PICKUP_RUN_SEQ,
  STOPPED_AT_SEQ,
  STOPPED_RUN_SEQ,
  stoppedRunEnvelopes,
  stoppedRunPickup,
  stoppedRunTasks,
} from "../pipeline/client/fixtures";
import { recordedSessionId, recordedTaskIds, recordedTasks } from "../pipeline/client/recordedRun";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Envelope, PipelineRun, Task, Template } from "../router/types";
import styles from "./TasksDemo.module.css";

// No Router here: a scripted EventSource stands in for the live bus, and the
// run comes from the recorded engine run (pipeline/client/recordedRun.ts).
FakeEventSource.instances = [];
globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;

type DemoMode = "plan" | "live" | "past";

const params = new URLSearchParams(location.search);
const mode: DemoMode = (["plan", "live", "past"] as const).find((m) => m === params.get("mode")) ?? "past";
const view: RunBarView = params.get("view") === "graph" ? "graph" : "list";

const TEMPLATE: Template = {
  id: "default-implementation",
  name: "default-implementation",
  description: "Architect, implement, build, review, merge, verify.",
  version: 3,
  system: true,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  source: [
    "name: default-implementation",
    "stages:",
    "  - name: architect",
    "    agent: architect",
    "  - name: executor",
    "    agent: executor",
    "  - name: build",
    "    command: make build",
    "  - name: code_review",
    "    agent: reviewer",
    "    on_fail: { loop_back: executor, max_loops: 2, then: block }",
    "  - name: merge",
    "    command: mads merge",
    "  - name: verify",
    "    command: make test",
  ].join("\n"),
};

class DemoRouterClient extends FakeRouterClient {
  override async listTemplates(): Promise<Template[]> {
    return [TEMPLATE];
  }
}

// The recording ran in milliseconds against a fake provider; spread it out
// so durations read like a real run, ending (live: still going) a moment ago.
//
// Every mode tells one story (docs/PIPELINE-LEASES-DESIGN.md): Run 1 was
// stopped with flaky api's code_review in flight, so flaky api is
// Interrupted. Past shows Run 1 · stopped; Plan shows flaky api will resume
// at code_review; live is Run 2, which picked it up.
const STEP_MS = 23_000;

function envelopes(): Envelope[] {
  const all = mode === "live" ? pickedUpRunEnvelopes() : stoppedRunEnvelopes();
  const first = STOPPED_RUN_SEQ;
  const last = all[all.length - 1].seq;
  const base = Date.now() - (last - first) * STEP_MS - (mode === "live" ? 40_000 : 3_600_000);
  return all.map((e) => ({ ...e, timestamp: new Date(base + Math.max(0, e.seq - first) * STEP_MS).toISOString() }));
}

function tasks(): Task[] {
  const copy = stoppedRunTasks();
  if (mode === "plan") {
    copy.push({
      ...structuredClone(recordedTasks[0]),
      id: "tsk_demo_draft",
      title: "docs for the cli",
      status: "ready",
      draft: true,
      dependencies: [recordedTaskIds.cli],
      notes: [],
    });
  }
  // No dependencies: the graph's tray holds it.
  copy.push({
    ...structuredClone(recordedTasks[0]),
    id: "tsk_demo_tray",
    title: "release notes",
    status: "ready",
    draft: true,
    dependencies: [],
    notes: [],
  });
  return copy;
}

const envs = envelopes();
const at = (seq: number) => envs.find((e) => e.seq === seq)!.timestamp;
const runs: PipelineRun[] = [
  {
    session_id: recordedSessionId,
    run_seq: STOPPED_RUN_SEQ,
    run_id: "run_stopped",
    started_at: at(STOPPED_RUN_SEQ),
    ended_at: at(STOPPED_AT_SEQ + 1),
    outcome: "cancelled",
    tasks_done: 1,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
  },
];
if (mode === "live") {
  runs.push({
    session_id: recordedSessionId,
    run_seq: PICKUP_RUN_SEQ,
    run_id: "run_pickup",
    started_at: at(PICKUP_RUN_SEQ),
    outcome: "running",
    tasks_done: 0,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
  });
}

const demoTasks = tasks();
const routerClient = new DemoRouterClient(
  { ...fixtureSessions, [recordedSessionId]: { ...fixtureSessions["session-active"], id: recordedSessionId } },
  undefined,
  undefined,
  undefined,
  demoTasks,
);
const pipelineClient = new FakePipelineClient(demoTasks.map((t) => ({ ...t })));
// `mads merge` prints nothing: its log is the lens's empty-output state.
pipelineClient.outputChunksFor = (_taskId, target) => (target?.stage === "merge" ? [] : undefined);
pipelineClient.runs = runs;
pipelineClient.runEvents = envs;
// Live: Run 2 holds flaky api's lease, so nothing is Interrupted.
pipelineClient.pickup = mode === "live" ? { shared_directory: false, tasks: [] } : stoppedRunPickup;
const focusRun = mode === "plan" ? { run: "new" as const } : undefined;

function href(next: { mode?: DemoMode; view?: RunBarView }) {
  const q = new URLSearchParams({ mode: next.mode ?? mode, view: next.view ?? view });
  return `?${q}`;
}

/**
 * Dev-only: the Tasks tab against the recorded run, no backend. `?mode=`
 * plan | live | past and `?view=` list | graph pick the starting state
 * (Start and Stop are recorded by the fake client and change nothing).
 */
export function TasksDemo() {
  return (
    <div className={styles.demo}>
      <nav className={styles.switch} aria-label="Demo state">
        <span className={styles.label}>Run</span>
        {(["plan", "live", "past"] as const).map((m) => (
          <a key={m} href={href({ mode: m })} aria-current={m === mode || undefined}>
            {m}
          </a>
        ))}
        <span className={styles.label}>View</span>
        {(["list", "graph"] as const).map((v) => (
          <a key={v} href={href({ view: v })} aria-current={v === view || undefined}>
            {v}
          </a>
        ))}
      </nav>
      <div className={styles.main}>
        <TasksTab
          sessionId={recordedSessionId}
          routerClient={routerClient}
          pipelineClient={pipelineClient}
          initialView={view}
          focusRun={focusRun}
          onOpenFile={(path) => console.log("open file", path)}
        />
      </div>
    </div>
  );
}
