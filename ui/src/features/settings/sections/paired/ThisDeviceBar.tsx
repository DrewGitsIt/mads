import { Button } from "../../../../components/Button/Button";
import { FingerprintChip } from "../../../../components/Fingerprint/Fingerprint";
import { KindIcon } from "../../../../components/KindIcon/KindIcon";
import type { SelfDevice } from "../../../../router/devices";
import { kindOf, platformLabel } from "./model";
import styles from "./PairedDevices.module.css";

export interface ThisDeviceBarProps {
  self: SelfDevice | undefined;
  /** Whether another device lets this one administer it (Introduce needs one). */
  canIntroduce: boolean;
  onIntroduce: () => void;
  /** Pair with a code: the fallback that always works. */
  onPair: () => void;
  /** Add devices: the one entry point to finding devices (setup's step 2). */
  onAdd: () => void;
  /** A mesh network is chosen: the actions show. */
  configured: boolean;
}

/**
 * The section's head: this device (kind, name, short fingerprint) and,
 * once a mesh network is chosen, Pair with a code and Add devices (and
 * Introduce a device… when another device lets this one administer it).
 * Before that, the empty state below it has the way in. Narrow, the
 * buttons wrap under the name.
 */
export function ThisDeviceBar({ self, canIntroduce, onIntroduce, onPair, onAdd, configured }: ThisDeviceBarProps) {
  return (
    <div className={styles.bar}>
      <div className={styles.who}>
        <KindIcon kind={kindOf(self?.platform)} label={platformLabel(self?.platform)} />
        <span className={styles.whoLabel}>This device</span>
        <span className={styles.whoName}>{self?.name ?? "…"}</span>
        {self?.fingerprint && (
          <FingerprintChip
            fingerprint={self.fingerprint}
            label={self.name}
            help="This device's. Other devices check it when you pair."
          />
        )}
      </div>
      {configured && (
      <div className={styles.barActions}>
        {canIntroduce && (
          <Button variant="ghost" onClick={onIntroduce}>
            Introduce a device…
          </Button>
        )}
        <Button variant="ghost" onClick={onPair}>
          Pair with a code
        </Button>
        <Button variant="primary" onClick={onAdd}>
          Add devices
        </Button>
      </div>
      )}
    </div>
  );
}
