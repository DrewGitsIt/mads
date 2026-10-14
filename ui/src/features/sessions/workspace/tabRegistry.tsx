import type { ComponentType } from "react";
import { ConversationTab } from "../../../conversation/ConversationTab";
import { ContextRail } from "../../../context-rail/ContextRail";
import { ChangesTab } from "../../../files/ChangesTab";
import type { ChangesFocus } from "../../../files/changesModel";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import { PipelineTab } from "../../../pipeline/PipelineTab";
import type { RouterClient } from "../../../router/client";
import { TasksTab } from "../../../tasks/TasksTab";
import { useTaskStore } from "../../../tasks/useTaskStore";
import styles from "./ConversationTabView.module.css";

export type WorkspaceTabId = "conversation" | "tasks" | "files" | "pipeline";

export interface WorkspaceTabContext {
  sessionId: string;
  /** Whether this tab is the one showing — tabs stay mounted (hidden) after their first visit. */
  active?: boolean;
  routerClient: RouterClient;
  pipelineClient: PipelineClient;
  /** Set for a session just created from the Launchpad — only ConversationTabView consumes these. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
  /** Open a changed file's diff — the Conversation tab's Commit dialog calls it; the workspace routes to the Changes tab (internal id "files"). */
  onOpenFile?: (path: string) => void;
  /** File to select in the Changes tab's diff viewer, set by onOpenFile. */
  focusFilePath?: string;
  /** Open the Changes tab on a file and/or the rail's tier and turn — the Context Rail's Changes list. */
  onOpenChanges?: (focus: ChangesFocus) => void;
  /** What onOpenChanges asked for. A new object each time, so a repeat click re-applies. */
  focusChanges?: ChangesFocus;
  /** Open one file's diff in a recorded commit — the Conversation tab's auto-merged Land card; routes to the Changes tab. */
  onOpenCommitFile?: (hash: string, path: string) => void;
  /** Commit file to select in the Changes tab, set by onOpenCommitFile. A new object each time, so a repeat click re-selects. */
  focusCommitFile?: { hash: string; path: string };
  /** Open one of this session's delegates (harness subagents) as a full-pane view — the dock's "Open as page". */
  onOpenDelegate?: (harnessId: string) => void;
  /** Open one of this session's tasks — the composer's "/task" toast link; the workspace routes to the Tasks tab. */
  onOpenTask?: (taskId: string) => void;
  /** Task to open in the Tasks tab's detail sheet, set by onOpenTask. A new object each time, so a repeat click re-opens. */
  focusTask?: { id: string };
}

export interface WorkspaceTabDef {
  id: WorkspaceTabId;
  label: string;
  component: ComponentType<WorkspaceTabContext>;
}

// Context Rail is mounted here, as a sibling to ConversationTab's chat/
// terminal content, so it stays constant across the internal display-mode
// toggle (spec: "identical in chat and terminal") rather than living inside
// ConversationTab itself. Owns its own useTaskStore instance — same
// duplicate-fetch tradeoff TasksDemo.tsx documents, acceptable until a
// container-level store is worth lifting.
function ConversationTabView({
  sessionId,
  routerClient,
  provisioning,
  onProvisioningDone,
  pendingFirstMessage,
  onPendingFirstMessageHandled,
  onOpenFile,
  onOpenChanges,
  onOpenCommitFile,
  onOpenDelegate,
  onOpenTask,
}: WorkspaceTabContext) {
  const { tasks, tasksById } = useTaskStore(routerClient, sessionId);
  return (
    <div className={styles.row}>
      <div className={styles.main}>
        <ConversationTab
          sessionId={sessionId}
          routerClient={routerClient}
          provisioning={provisioning}
          onProvisioningDone={onProvisioningDone}
          pendingFirstMessage={pendingFirstMessage}
          onPendingFirstMessageHandled={onPendingFirstMessageHandled}
          onOpenFile={onOpenFile}
          onOpenCommitFile={onOpenCommitFile}
          onOpenDelegate={onOpenDelegate}
          onOpenTask={onOpenTask}
        />
      </div>
      <ContextRail
        sessionId={sessionId}
        routerClient={routerClient}
        tasks={tasks}
        tasksById={tasksById}
        onOpenFile={onOpenFile}
        onOpenChanges={onOpenChanges}
      />
    </div>
  );
}

function TasksTabView({ sessionId, routerClient, pipelineClient, focusTask }: WorkspaceTabContext) {
  return (
    <TasksTab
      sessionId={sessionId}
      routerClient={routerClient}
      pipelineClient={pipelineClient}
      focusTask={focusTask}
    />
  );
}

// The Changes tab (formerly "Files"); its internal id stays "files", which
// e2e selectors and DOM ids (#workspace-panel-files) key on.
function ChangesTabView({
  sessionId,
  routerClient,
  focusFilePath,
  focusCommitFile,
  focusChanges,
  active,
}: WorkspaceTabContext) {
  return (
    <ChangesTab
      sessionId={sessionId}
      routerClient={routerClient}
      focusPath={focusFilePath}
      focusCommit={focusCommitFile}
      focusView={focusChanges}
      active={active}
    />
  );
}

function PipelineTabView({ sessionId, pipelineClient }: WorkspaceTabContext) {
  return <PipelineTab sessionId={sessionId} pipelineClient={pipelineClient} />;
}

export const WORKSPACE_TABS: WorkspaceTabDef[] = [
  { id: "conversation", label: "Conversation", component: ConversationTabView },
  { id: "tasks", label: "Tasks", component: TasksTabView },
  { id: "files", label: "Changes", component: ChangesTabView },
  { id: "pipeline", label: "Pipeline", component: PipelineTabView },
];
