import { Button } from "../../../../components/Button/Button";
import { FingerprintChip } from "../../../../components/Fingerprint/Fingerprint";
import { KindIcon } from "../../../../components/KindIcon/KindIcon";
import { Tooltip } from "../../../../components/Tooltip/Tooltip";
import type { SelfDevice } from "../../../../router/devices";
import { kindOf, platformLabel } from "./model";
import styles from "./PairedDevices.module.css";

export interface ThisDeviceBarProps {
  self: SelfDevice | undefined;
  /** Whether another device lets this one administer it (Introduce needs one). */
  canIntroduce: boolean;
  /** Why it can't, when there's more to say (a tailnet policy capping an administer). */
  introduceWhy?: string;
  onIntroduce: () => void;
  onPair: () => void;
}

/** The section's head: this device (kind, name, short fingerprint), Introduce a device…, Pair a device. */
export function ThisDeviceBar({ self, canIntroduce, introduceWhy, onIntroduce, onPair }: ThisDeviceBarProps) {
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
      <div className={styles.barActions}>
        {canIntroduce ? (
          <Button variant="ghost" onClick={onIntroduce}>
            Introduce a device…
          </Button>
        ) : (
          <Tooltip
            tip={`You can introduce devices to a device that lets this one administer mads.${introduceWhy ? ` ${introduceWhy}` : ""}`}
            align="end"
            focusable={false}
          >
            <Button variant="ghost" aria-disabled="true" className={styles.ariaDisabled} onClick={(e) => e.preventDefault()}>
              Introduce a device…
            </Button>
          </Tooltip>
        )}
        <Button variant="primary" onClick={onPair}>
          Pair a device
        </Button>
      </div>
    </div>
  );
}
