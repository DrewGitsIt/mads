import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { RegistryEntry } from "../../router/types";
import styles from "./RegistryEntryCard.module.css";

export interface RegistryEntryCardProps {
  entry: RegistryEntry;
  installed: boolean;
  pending: boolean;
  onInstall: (version: string) => void;
  onUpdate: (version: string) => void;
}

export function RegistryEntryCard({
  entry,
  installed,
  pending,
  onInstall,
  onUpdate,
}: RegistryEntryCardProps) {
  const [version, setVersion] = useState(entry.latest);

  return (
    <Card className={styles.card}>
      <div className={styles.name}>
        {entry.name}
        <Chip tone="neutral" className={styles.typeChip}>
          {entry.type}
        </Chip>
      </div>
      {entry.description && <div className={styles.description}>{entry.description}</div>}
      <div className={styles.actions}>
        <select
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          className={styles.versionSelect}
          aria-label={`${entry.name} version`}
        >
          {entry.versions.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
        <Button
          variant="primary"
          size="sm"
          disabled={pending}
          onClick={() => (installed ? onUpdate(version) : onInstall(version))}
        >
          {installed ? "Update" : "Install"}
        </Button>
      </div>
    </Card>
  );
}
