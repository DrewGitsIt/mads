import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { AgentCard } from "./AgentCard";
import { AgentEditor } from "./AgentEditor";
import { useAgents } from "./useAgents";
import type { Agent } from "../../router/types";
import styles from "./AgentsShelf.module.css";

export function AgentsShelf() {
  const { agents, loading, error, createAgent, updateAgent, deleteAgent } = useAgents();
  const [editing, setEditing] = useState<Agent | null | "new">(null);

  return (
    <div className={styles.shelf}>
      <div className={styles.header}>
        <h2 className={styles.title}>Agents</h2>
        <Button variant="primary" onClick={() => setEditing("new")}>
          New agent
        </Button>
      </div>
      {error && <Toast tone="bad" title="Couldn't load agents" description={error} />}
      {!loading && agents.length === 0 && (
        <div className={styles.empty}>No agents yet.</div>
      )}
      <div className={styles.grid}>
        {agents.map((agent) => (
          <AgentCard key={agent.id} agent={agent} onClick={() => setEditing(agent)} />
        ))}
      </div>
      {editing && (
        <AgentEditor
          agent={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onCreate={createAgent}
          onUpdate={updateAgent}
          onDelete={deleteAgent}
        />
      )}
    </div>
  );
}
