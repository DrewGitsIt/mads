import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import { Toast } from "../../components/Toast/Toast";
import type { Capability } from "../../router/types";
import { CapabilityEditor } from "./CapabilityEditor";
import { HealthDot } from "./HealthDot";
import { useCapabilities } from "./useCapabilities";
import styles from "./CapabilitiesShelf.module.css";

export function CapabilitiesShelf() {
  const { capabilities, loading, error, createCapability, updateCapability, deleteCapability } =
    useCapabilities();
  const [editing, setEditing] = useState<Capability | null | "new">(null);

  return (
    <div className={styles.shelf}>
      <div className={styles.header}>
        <h2 className={styles.title}>Capabilities</h2>
        <Button variant="primary" onClick={() => setEditing("new")}>
          New capability
        </Button>
      </div>
      {error && <Toast tone="bad" title="Couldn't load capabilities" description={error} />}
      {!loading && capabilities.length === 0 && (
        <div className={styles.empty}>No capabilities yet.</div>
      )}
      <div className={styles.grid}>
        {capabilities.map((capability) => (
          <Card key={capability.id} hoverable className={styles.card}>
            <button type="button" className={styles.trigger} onClick={() => setEditing(capability)}>
              <div className={styles.name}>
                <HealthDot capabilityId={capability.id} health={capability.health} />
                {capability.name}
                <Chip tone="neutral" className={styles.typeChip}>
                  {capability.type}
                </Chip>
              </div>
              {capability.description && (
                <div className={styles.description}>{capability.description}</div>
              )}
            </button>
          </Card>
        ))}
      </div>
      {editing && (
        <CapabilityEditor
          capability={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onCreate={createCapability}
          onUpdate={updateCapability}
          onDelete={deleteCapability}
        />
      )}
    </div>
  );
}
