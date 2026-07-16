import { useState } from "react";
import { Button } from "../../components/Button/Button";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import type { Agent, Capability, RegistryPublishItem, Template } from "../../router/types";
import styles from "./PublishDialog.module.css";

export interface PublishDialogProps {
  agents: Agent[];
  capabilities: Capability[];
  templates: Template[];
  pending: boolean;
  error: string | null;
  onPublish: (items: RegistryPublishItem[]) => Promise<void>;
  onClose: () => void;
}

function itemKey(type: RegistryPublishItem["type"], id: string): string {
  return `${type}:${id}`;
}

/**
 * Lists every local agent/capability/template as an explicit checkbox row
 * (MADS-UX #22 — nothing pre-checked) and publishes the checked set as a
 * batch. Publish stops at the first item that fails validation on the
 * remote side (pkg/job's RegistryPublish), so a failure is surfaced inline
 * rather than swallowed — the dialog stays open so the user can see which
 * item to fix.
 */
export function PublishDialog({
  agents,
  capabilities,
  templates,
  pending,
  error,
  onPublish,
  onClose,
}: PublishDialogProps) {
  const [checked, setChecked] = useState<Set<string>>(new Set());

  const toggle = (type: RegistryPublishItem["type"], id: string) => {
    setChecked((prev) => {
      const next = new Set(prev);
      const key = itemKey(type, id);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  const handlePublish = async () => {
    const items: RegistryPublishItem[] = [...checked].map((key) => {
      const [type, id] = key.split(":") as [RegistryPublishItem["type"], string];
      return { type, id };
    });
    try {
      await onPublish(items);
      onClose();
    } catch {
      // error is surfaced via the `error` prop — stay open so the user can retry.
    }
  };

  return (
    <SideSheet>
      <SideSheetHeader>Publish to registry</SideSheetHeader>
      <SideSheetBody>
        <section className={styles.section}>
          <h3 className={styles.sectionTitle}>Agents</h3>
          {agents.length === 0 && <div className={styles.empty}>No agents yet.</div>}
          {agents.map((a) => (
            <label key={a.id} className={styles.row}>
              <input
                type="checkbox"
                checked={checked.has(itemKey("agent", a.id))}
                onChange={() => toggle("agent", a.id)}
              />
              {a.name}
            </label>
          ))}
        </section>
        <section className={styles.section}>
          <h3 className={styles.sectionTitle}>Capabilities</h3>
          {capabilities.length === 0 && <div className={styles.empty}>No capabilities yet.</div>}
          {capabilities.map((c) => (
            <label key={c.id} className={styles.row}>
              <input
                type="checkbox"
                checked={checked.has(itemKey("capability", c.id))}
                onChange={() => toggle("capability", c.id)}
              />
              {c.name}
            </label>
          ))}
        </section>
        <section className={styles.section}>
          <h3 className={styles.sectionTitle}>Templates</h3>
          {templates.length === 0 && <div className={styles.empty}>No templates yet.</div>}
          {templates.map((t) => (
            <label key={t.id} className={styles.row}>
              <input
                type="checkbox"
                checked={checked.has(itemKey("template", t.id))}
                onChange={() => toggle("template", t.id)}
              />
              {t.name}
            </label>
          ))}
        </section>
        {error && <div className={styles.error}>{error}</div>}
      </SideSheetBody>
      <SideSheetFooter>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="primary"
          disabled={pending || checked.size === 0}
          onClick={() => void handlePublish()}
        >
          Publish
        </Button>
      </SideSheetFooter>
    </SideSheet>
  );
}
