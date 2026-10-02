import { useState } from "react";
import { Popover } from "../../components/Popover/Popover";
import type { PreflightCheck } from "../../router/types";
import styles from "./PreflightChecks.module.css";

const PROVIDER_PREFIX = "provider:";

export interface PreflightChecksProps {
  checks: PreflightCheck[];
  loading: boolean;
}

/**
 * The preflight check list shared by Settings > General and the wizard's
 * PreflightStep. Provider harnesses are optional, so a provider whose binary
 * wasn't found (`not_installed`) is hidden from the health list rather than
 * shown as a failure; the (i) disclosure names every provider preflight
 * probed, detected or not, with its install hint.
 */
export function PreflightChecks({ checks, loading }: PreflightChecksProps) {
  const [infoOpen, setInfoOpen] = useState(false);
  const shown = checks.filter((c) => !c.not_installed);
  const probed = checks.filter((c) => c.name.startsWith(PROVIDER_PREFIX));
  const noneDetected = !loading && probed.length > 0 && probed.every((c) => c.not_installed);

  return (
    <div className={styles.root}>
      <ul className={styles.checks}>
        {shown.map((check) => (
          <li key={check.name} className={styles.check}>
            <span aria-hidden="true">{check.healthy ? "✓" : "✗"}</span>
            {check.name}
            {!check.healthy && check.fix_hint && <span className={styles.hint}>{check.fix_hint}</span>}
          </li>
        ))}
        {noneDetected && (
          <li className={styles.check}>
            <span aria-hidden="true">✗</span>
            no agent harness detected
            <span className={styles.hint}>install one of the providers listed under (i)</span>
          </li>
        )}
      </ul>
      {probed.length > 0 && (
        <Popover
          open={infoOpen}
          onClose={() => setInfoOpen(false)}
          className={styles.info}
          anchor={
            <button
              type="button"
              className={styles.infoButton}
              aria-label="Providers checked"
              aria-expanded={infoOpen}
              onClick={() => setInfoOpen((open) => !open)}
            >
              i
            </button>
          }
        >
          <div className={styles.infoTitle}>Providers checked</div>
          <ul className={styles.probed}>
            {probed.map((check) => (
              <li key={check.name}>
                <span className={styles.probedName}>{check.name.slice(PROVIDER_PREFIX.length)}</span>
                <span>{check.not_installed ? "not detected" : (check.detail ?? "detected")}</span>
                {check.not_installed && check.fix_hint && (
                  <span className={styles.probedHint}>{check.fix_hint}</span>
                )}
              </li>
            ))}
          </ul>
        </Popover>
      )}
    </div>
  );
}
