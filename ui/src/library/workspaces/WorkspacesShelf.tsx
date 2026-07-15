import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Toast } from "../../components/Toast/Toast";
import type { Workspace } from "../../router/types";
import { useWorkspaces } from "./useWorkspaces";
import { WorkspaceEditor } from "./WorkspaceEditor";
import styles from "./WorkspacesShelf.module.css";

export function WorkspacesShelf() {
  const { workspaces, loading, error, createWorkspace, updateWorkspace, deleteWorkspace } =
    useWorkspaces();
  const [editing, setEditing] = useState<Workspace | null | "new">(null);

  return (
    <div className={styles.shelf}>
      <div className={styles.header}>
        <h2 className={styles.title}>Workspaces</h2>
        <Button variant="primary" onClick={() => setEditing("new")}>
          New workspace
        </Button>
      </div>
      {error && <Toast tone="bad" title="Couldn't load workspaces" description={error} />}
      {!loading && workspaces.length === 0 && (
        <div className={styles.empty}>No workspaces yet.</div>
      )}
      <div className={styles.grid}>
        {workspaces.map((workspace) => (
          <Card key={workspace.id} hoverable className={styles.card}>
            <button type="button" className={styles.trigger} onClick={() => setEditing(workspace)}>
              <div className={styles.name}>{workspace.name}</div>
              {workspace.description && (
                <div className={styles.description}>{workspace.description}</div>
              )}
              <div className={styles.repoCount}>
                {workspace.repos?.length ?? 0} repo{workspace.repos?.length === 1 ? "" : "s"}
              </div>
            </button>
          </Card>
        ))}
      </div>
      {editing && (
        <WorkspaceEditor
          workspace={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onCreate={createWorkspace}
          onUpdate={updateWorkspace}
          onDelete={deleteWorkspace}
        />
      )}
    </div>
  );
}
