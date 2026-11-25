import { Button } from "../../../../components/Button/Button";
import { SettingsCard } from "../../SettingsCard";
import type { SelfMesh } from "../../../../router/devices";
import { tailscaleHere } from "./network";
import styles from "./Mesh.module.css";

export interface MeshEmptyStateProps {
  mesh: SelfMesh | undefined;
  /** Set up with Tailscale: one click, then Add devices. */
  onTailscale: () => void;
  /** Other networks… (with Tailscale here), or Set up mesh. */
  onSetUp: () => void;
  /** I have a pairing code: joiner-first. */
  onCode: () => void;
  busy?: boolean;
}

/**
 * A device with no mesh network yet (U1): what mesh is for, and how to
 * start. With Tailscale here, its primary is Set up with Tailscale.
 */
export function MeshEmptyState({ mesh, onTailscale, onSetUp, onCode, busy }: MeshEmptyStateProps) {
  const ts = tailscaleHere(mesh);
  return (
    <SettingsCard className={styles.emptyCard}>
      <div>
        <h3 className={styles.emptyTitle}>Use mads across your devices</h3>
        <p className={styles.emptyBody}>
          See and drive sessions on your other computers, and let them use this one. First, choose how your devices reach each other.
        </p>
        <ol className={styles.emptySteps}>
          <li>
            <i>1</i>Pick a network
          </li>
          <li>
            <i>2</i>Add your devices
          </li>
          <li>
            <i>3</i>Approve each one
          </li>
        </ol>
      </div>
      <div className={styles.emptyActions}>
        {ts ? (
          <>
            <Button variant="primary" onClick={onTailscale} disabled={busy}>
              {busy ? "Setting up…" : "Set up with Tailscale"}
            </Button>
            <Button variant="ghost" onClick={onSetUp}>
              Other networks…
            </Button>
          </>
        ) : (
          <Button variant="primary" onClick={onSetUp}>
            Set up mesh
          </Button>
        )}
        <Button variant="ghost" onClick={onCode}>
          I have a pairing code
        </Button>
      </div>
    </SettingsCard>
  );
}
