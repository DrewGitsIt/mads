import { useState } from "react";
import { Chip } from "../components/Chip/Chip";
import { ContentColumn } from "../components/ContentColumn/ContentColumn";
import { FLAGS } from "../featureFlags";
import { AgentsShelf } from "./agents/AgentsShelf";
import { CapabilitiesShelf } from "./capabilities/CapabilitiesShelf";
import { WorkspacesShelf } from "./workspaces/WorkspacesShelf";
import { TemplatesShelf } from "./templates/TemplatesShelf";
import { RegistryShelf } from "./registry/RegistryShelf";
import styles from "./LibrarySurface.module.css";

type ShelfId = "personas" | "capabilities" | "workspaces" | "templates" | "registry";

// Redesign §6: the "Agents" shelf becomes the "Personas" shelf — the same
// records, a narrower concept (a persona is the registry object with prompt
// + allow/deny intents + quadrant + tags + capability refs, *no harness
// fields*). The AgentsShelf component is unchanged — it lists the persona
// registry (contracts.Agent) and hosts the persona editor.
const SHELVES: { id: ShelfId; label: string }[] = [
  { id: "personas", label: "Personas" },
  { id: "capabilities", label: "Capabilities" },
  { id: "workspaces", label: "Workspaces" },
  { id: "templates", label: "Templates" },
];

/** Top-level Library surface (registered in App.tsx) — owns shelf nav, MADS-UX §3.5. */
export function LibrarySurface() {
  const [activeShelf, setActiveShelf] = useState<ShelfId>("personas");
  const shelves = FLAGS.remoteRegistry
    ? [...SHELVES, { id: "registry" as ShelfId, label: "Registry" }]
    : SHELVES;

  return (
    <div className={styles.surface}>
      <nav className={styles.nav}>
        {shelves.map((shelf) => (
          <Chip
            key={shelf.id}
            tone="neutral"
            pressed={shelf.id === activeShelf}
            onClick={() => setActiveShelf(shelf.id)}
          >
            {shelf.label}
          </Chip>
        ))}
      </nav>
      <main className={styles.main}>
        <ContentColumn variant="wide">
          {activeShelf === "personas" && <AgentsShelf />}
          {activeShelf === "capabilities" && <CapabilitiesShelf />}
          {activeShelf === "workspaces" && <WorkspacesShelf />}
          {activeShelf === "templates" && <TemplatesShelf />}
          {activeShelf === "registry" && <RegistryShelf />}
        </ContentColumn>
      </main>
    </div>
  );
}
