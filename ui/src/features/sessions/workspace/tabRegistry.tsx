import type { ComponentType } from "react";
import { ConversationTab } from "../../../conversation/ConversationTab";
import type { PtyBridge } from "../../../conversation/TerminalRenderer/ptyBridge";
import { FilesTab } from "../../../files/FilesTab";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import { PipelineTab } from "../../../pipeline/PipelineTab";
import type { RouterClient } from "../../../router/client";
import { TasksTab } from "../../../tasks/TasksTab";

export type WorkspaceTabId = "conversation" | "tasks" | "files" | "pipeline";

export interface WorkspaceTabContext {
  sessionId: string;
  routerClient: RouterClient;
  pipelineClient: PipelineClient;
  ptyBridge?: PtyBridge;
  /** Set for a session just created from the Launchpad — only ConversationTabView consumes these. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
}

export interface WorkspaceTabDef {
  id: WorkspaceTabId;
  label: string;
  component: ComponentType<WorkspaceTabContext>;
}

function ConversationTabView({
  sessionId,
  routerClient,
  ptyBridge,
  provisioning,
  onProvisioningDone,
  pendingFirstMessage,
  onPendingFirstMessageHandled,
}: WorkspaceTabContext) {
  return (
    <ConversationTab
      sessionId={sessionId}
      routerClient={routerClient}
      ptyBridge={ptyBridge}
      provisioning={provisioning}
      onProvisioningDone={onProvisioningDone}
      pendingFirstMessage={pendingFirstMessage}
      onPendingFirstMessageHandled={onPendingFirstMessageHandled}
    />
  );
}

function TasksTabView({ sessionId, routerClient, pipelineClient }: WorkspaceTabContext) {
  return <TasksTab sessionId={sessionId} routerClient={routerClient} pipelineClient={pipelineClient} />;
}

function FilesTabView({ sessionId, routerClient }: WorkspaceTabContext) {
  // taskId intentionally omitted — this is the session-level mount, no
  // single task in scope; FilesTab already handles taskId being undefined.
  return <FilesTab sessionId={sessionId} routerClient={routerClient} />;
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
