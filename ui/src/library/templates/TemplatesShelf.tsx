import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import { Toast } from "../../components/Toast/Toast";
import { router } from "../../router/client";
import type { Template } from "../../router/types";
import { TemplateEditor } from "./TemplateEditor";
import { useTemplates } from "./useTemplates";
import styles from "./TemplatesShelf.module.css";

export function TemplatesShelf() {
  const { templates, loading, error, createTemplate, updateTemplate, deleteTemplate } = useTemplates();
  const [editing, setEditing] = useState<Template | null | "new">(null);
  const [invalidIds, setInvalidIds] = useState<Set<string>>(new Set());

  // One-shot sweep, fetched once on mount — badges invalid templates so a
  // now-broken template (e.g. hand-edited YAML that drifted from the
  // grammar) is visible before anyone opens it. Best-effort: the endpoint
  // may 404/error and the shelf must still render every template normally.
  useEffect(() => {
    let cancelled = false;
    router
      .validateAllTemplates()
      .then((results) => {
        if (cancelled) return;
        setInvalidIds(new Set(results.filter((r) => !r.valid).map((r) => r.id)));
      })
      .catch(() => {
        // No badges this session — not a reason to hide the template list.
      });
    return () => {
      cancelled = true;
    };
  }, []);

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
                <Chip tone="neutral" className={styles.versionChip}>
                  v{template.version}
                </Chip>
                {invalidIds.has(template.id) && <Chip tone="bad">Invalid</Chip>}
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
