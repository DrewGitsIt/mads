import type { ComponentType } from "react";
import { ConversationTab } from "../../../conversation/ConversationTab";
import { ContextRail } from "../../../context-rail/ContextRail";
import { FilesTab } from "../../../files/FilesTab";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import { PipelineTab } from "../../../pipeline/PipelineTab";
import type { RouterClient } from "../../../router/client";
import { TasksTab } from "../../../tasks/TasksTab";
import { useTaskStore } from "../../../tasks/useTaskStore";
import styles from "./ConversationTabView.module.css";

export type WorkspaceTabId = "conversation" | "tasks" | "files" | "pipeline";

export interface WorkspaceTabContext {
  sessionId: string;
  routerClient: RouterClient;
  pipelineClient: PipelineClient;
  /** Set for a session just created from the Launchpad — only ConversationTabView consumes these. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
  /** Open a touched file's diff — the Conversation tab's rail and Commit dialog call it; the workspace routes to the Files tab. */
  onOpenFile?: (path: string) => void;
  /** File to select in the Files tab's diff viewer, set when opened from the rail. */
  focusFilePath?: string;
  /** Open one file's diff in a recorded commit — the Conversation tab's auto-merged Land card; routes to the Files tab. */
  onOpenCommitFile?: (hash: string, path: string) => void;
  /** Commit file to select in the Files tab, set by onOpenCommitFile. A new object each time, so a repeat click re-selects. */
  focusCommitFile?: { hash: string; path: string };
  /** Open one of this session's delegates (harness subagents) as a full-pane view — the dock's "Open as page". */
  onOpenDelegate?: (harnessId: string) => void;
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
  onOpenCommitFile,
  onOpenDelegate,
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
        />
      </div>
      <ContextRail
        sessionId={sessionId}
        routerClient={routerClient}
        tasks={tasks}
        tasksById={tasksById}
        onOpenFile={onOpenFile}
      />
    </div>
  );
}

function TasksTabView({ sessionId, routerClient, pipelineClient }: WorkspaceTabContext) {
  return <TasksTab sessionId={sessionId} routerClient={routerClient} pipelineClient={pipelineClient} />;
}

function FilesTabView({ sessionId, routerClient, focusFilePath, focusCommitFile }: WorkspaceTabContext) {
  // taskId intentionally omitted — this is the session-level mount, no
  // single task in scope; FilesTab already handles taskId being undefined.
  return (
    <FilesTab
      sessionId={sessionId}
      routerClient={routerClient}
      focusPath={focusFilePath}
      focusCommit={focusCommitFile}
    />
  );
}

function PipelineTabView({ sessionId, pipelineClient }: WorkspaceTabContext) {
  return <PipelineTab sessionId={sessionId} pipelineClient={pipelineClient} />;
}

export const WORKSPACE_TABS: WorkspaceTabDef[] = [
  { id: "conversation", label: "Conversation", component: ConversationTabView },
  { id: "tasks", label: "Tasks", component: TasksTabView },
  { id: "files", label: "Files", component: FilesTabView },
  { id: "pipeline", label: "Pipeline", component: PipelineTabView },
];
