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

type ShelfId = "agents" | "capabilities" | "workspaces" | "templates" | "registry";

const SHELVES: { id: ShelfId; label: string }[] = [
  { id: "agents", label: "Agents" },
  { id: "capabilities", label: "Capabilities" },
  { id: "workspaces", label: "Workspaces" },
  { id: "templates", label: "Templates" },
];

/** Top-level Library surface (registered in App.tsx) — owns shelf nav, MADS-UX §3.5. */
export function LibrarySurface() {
  const [activeShelf, setActiveShelf] = useState<ShelfId>("agents");
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
          {activeShelf === "agents" && <AgentsShelf />}
          {activeShelf === "capabilities" && <CapabilitiesShelf />}
          {activeShelf === "workspaces" && <WorkspacesShelf />}
          {activeShelf === "templates" && <TemplatesShelf />}
          {activeShelf === "registry" && <RegistryShelf />}
        </ContentColumn>
      </main>
    </div>
  );
}
