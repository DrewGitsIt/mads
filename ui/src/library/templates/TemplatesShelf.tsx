import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import { Toast } from "../../components/Toast/Toast";
import type { Template } from "../../router/types";
import { TemplateEditor } from "./TemplateEditor";
import { useTemplates } from "./useTemplates";
import styles from "./TemplatesShelf.module.css";

export function TemplatesShelf() {
  const { templates, loading, error, createTemplate, updateTemplate, deleteTemplate } = useTemplates();
  const [editing, setEditing] = useState<Template | null | "new">(null);

  return (
    <div className={styles.shelf}>
      <div className={styles.header}>
        <h2 className={styles.title}>Pipeline templates</h2>
        <Button variant="primary" onClick={() => setEditing("new")}>
          New template
        </Button>
      </div>
      {error && <Toast tone="bad" title="Couldn't load templates" description={error} />}
      {!loading && templates.length === 0 && (
        <div className={styles.empty}>No templates yet.</div>
      )}
      <div className={styles.grid}>
        {templates.map((template) => (
          <Card key={template.id} hoverable className={styles.card}>
            <button type="button" className={styles.trigger} onClick={() => setEditing(template)}>
              <div className={styles.name}>
                {template.name}
                {template.system && (
                  <Chip tone="info" className={styles.systemChip}>
                    System
                  </Chip>
                )}
              </div>
              {template.description && (
                <div className={styles.description}>{template.description}</div>
              )}
            </button>
          </Card>
        ))}
      </div>
      {editing && (
        <TemplateEditor
          template={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onCreate={createTemplate}
          onUpdate={updateTemplate}
          onDelete={deleteTemplate}
        />
      )}
    </div>
  );
}
