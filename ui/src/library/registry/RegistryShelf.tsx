import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Banner } from "../../components/Banner/Banner";
import { Toast, useDismissible } from "../../components/Toast/Toast";
import { useAgents } from "../agents/useAgents";
import { useCapabilities } from "../capabilities/useCapabilities";
import { useTemplates } from "../templates/useTemplates";
import { PublishDialog } from "./PublishDialog";
import { RegistryEntryCard } from "./RegistryEntryCard";
import type { RegistryEntry } from "../../router/types";
import { useRegistryIndex } from "./useRegistryIndex";
import { useRegistryJob } from "./useRegistryJob";
import styles from "./RegistryShelf.module.css";

function isInstalled(
  entry: RegistryEntry,
  agentNames: Set<string>,
  capabilityNames: Set<string>,
  templateNames: Set<string>,
): boolean {
  if (entry.type === "agent") return agentNames.has(entry.name);
  if (entry.type === "capability") return capabilityNames.has(entry.name);
  return templateNames.has(entry.name);
}

/** Browse/install/update/publish for the remote registry (spec §12.9, W7.6) — flag-gated. */
export function RegistryShelf() {
  const { entries, loading, error, query, setQuery, refetch } = useRegistryIndex();
  const { agents } = useAgents();
  const { capabilities } = useCapabilities();
  const { templates } = useTemplates();
  const { pending, error: jobError, install, update, publish } = useRegistryJob();
  const [publishing, setPublishing] = useState(false);
  const [shownJobError, dismissJobError] = useDismissible(jobError);

  const agentNames = new Set(agents.map((a) => a.name));
  const capabilityNames = new Set(capabilities.map((c) => c.name));
  const templateNames = new Set(templates.map((t) => t.name));

  return (
    <div className={styles.shelf}>
      <div className={styles.header}>
        <h2 className={styles.title}>Registry</h2>
        <div className={styles.headerActions}>
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search the registry…"
            className={styles.search}
            aria-label="Search the registry"
          />
          <Button variant="primary" onClick={() => setPublishing(true)}>
            Publish
          </Button>
        </div>
      </div>
      <Banner
        active={!!error}
        tone="bad"
        variant="inline"
        title="Couldn't load the registry"
        description={error ?? undefined}
        action={{ label: "Retry", onClick: refetch }}
      />
      {shownJobError && (
        <Toast tone="bad" title="Registry job failed" description={shownJobError} onDismiss={dismissJobError} />
      )}
      {!loading && entries.length === 0 && <div className={styles.empty}>No matching items.</div>}
      <div className={styles.grid}>
        {entries.map((entry) => (
          <RegistryEntryCard
            key={`${entry.type}/${entry.name}`}
            entry={entry}
            installed={isInstalled(entry, agentNames, capabilityNames, templateNames)}
            pending={pending}
            onInstall={(version) => {
              install(entry.type, entry.name, version).catch(() => {});
            }}
            onUpdate={(version) => {
              update(entry.type, entry.name, version).catch(() => {});
            }}
          />
        ))}
      </div>
      {publishing && (
        <PublishDialog
          agents={agents}
          capabilities={capabilities}
          templates={templates}
          pending={pending}
          error={jobError}
          onClose={() => setPublishing(false)}
          onPublish={async (items) => {
            await publish(items);
            refetch();
          }}
        />
      )}
    </div>
  );
}
