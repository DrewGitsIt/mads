import type { KeyboardEvent } from "react";
import { SettingTag } from "../../../SettingRow";
import type { NetworkView } from "../../../../../router/devices";
import { knockLine, meaningLine, networkIcon, networkTitle } from "../network";
import { NetIcon } from "../NetIcon";
import styles from "../Mesh.module.css";

export interface NetworkChoiceProps {
  network: NetworkView;
  checked: boolean;
  onPick: () => void;
  onKeyDown?: (e: KeyboardEvent<HTMLButtonElement>) => void;
}

/**
 * One network as a radio card (U1 step 1): what it is, what it means, and
 * who can try to connect; Recommended for Tailscale, a warning for a
 * network that looks shared.
 */
export function NetworkChoice({ network: n, checked, onPick, onKeyDown }: NetworkChoiceProps) {
  const knock = knockLine(n);
  return (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      tabIndex={checked ? 0 : -1}
      className={styles.choice}
      onClick={onPick}
      onKeyDown={onKeyDown}
      data-network={n.id}
    >
      <span className={styles.mark} aria-hidden="true" />
      <NetIcon icon={networkIcon(n)} />
      <span>
        <span className={styles.choiceTitle}>
          {networkTitle(n)}
          {n.recommended && <SettingTag tone="ok">Recommended</SettingTag>}
          {n.shared && <SettingTag tone="warn">Shared network</SettingTag>}
        </span>
        <span className={styles.meaning}>{meaningLine(n)}</span>
        <span className={styles.safety} data-warn={knock.warn || undefined}>
          {knock.text}
        </span>
      </span>
    </button>
  );
}
