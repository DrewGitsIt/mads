import type { ReactNode } from "react";
import { Button } from "../../../../components/Button/Button";
import { FingerprintGroups } from "../../../../components/Fingerprint/Fingerprint";
import { SegmentedControl } from "../../../../components/SegmentedControl/SegmentedControl";
import { Select } from "../../../../components/Select/Select";
import { InfoTip } from "../../../../components/Tooltip/Tooltip";
import { SettingsCard } from "../../SettingsCard";
import type { DeviceRow, PeerCeilings } from "../../../../router/devices";
import { ActivityList } from "./DeviceActivity";
import { dateTime, levelOf } from "./model";
import styles from "./PairedDevices.module.css";

export const TRUST_OPTIONS = [
  { value: "", label: "No limit" },
  { value: "auto_review", label: "Auto-review" },
  { value: "limited", label: "Limited" },
  { value: "read_only", label: "Read-only" },
];

function OptRow({ label, labelId, tag, help, children }: { label: ReactNode; labelId?: string; tag?: ReactNode; help?: ReactNode; children: ReactNode }) {
  return (
    <div className={styles.optRow}>
      <div>
        <div className={styles.optLabelLine}>
          <span className={styles.optLabel} id={labelId}>
            {label}
          </span>
          {tag}
        </div>
        {help && <div className={styles.optHelp}>{help}</div>}
      </div>
      <div className={styles.optControl}>{children}</div>
    </div>
  );
}

export interface DeviceDetailsProps {
  row: DeviceRow;
  rows: DeviceRow[];
  /** Saves new limits (the level unchanged). */
  onCeilings: (ceilings: PeerCeilings) => void;
  onRemove: () => void;
  busy?: boolean;
}

/**
 * A row's details, inline under it: its limits (Advanced options), its
 * fingerprint and pairing date, Remove device…, and its slice of the
 * activity log.
 */
export function DeviceDetails({ row, rows, onCeilings, onRemove, busy }: DeviceDetailsProps) {
  const level = levelOf(row);
  const ceilings = row.grant?.ceilings ?? {};
  const chatOn = level === "chat";
  const controlOn = level === "control" || level === "admin";
  const idp = `adv-${row.id}`;
  const c1 = ceilings.full_trust_chat === "refused" || ceilings.full_trust_chat === "held" ? "refused" : "allowed";
  return (
    <div className={styles.detail} id={`detail-${row.id}`}>
      <div>
        <div className={styles.detailTitle}>Advanced options</div>
        <SettingsCard className={styles.detailCard}>
          {row.fingerprint ? (
            <>
              <OptRow
                label="Messages to Full access sessions"
                labelId={`${idp}-chat-l`}
                tag={
                  <InfoTip label="Messages to Full access sessions">
                    Only matters while it may <b>chat with</b> mads here. Refused blocks its chat messages to sessions that
                    already run at Full access; other sessions are unaffected.
                  </InfoTip>
                }
                help={chatOn ? undefined : "Only matters at chat with."}
              >
                <SegmentedControl
                  value={c1}
                  options={[
                    { value: "allowed", label: "Allowed" },
                    { value: "refused", label: "Refused" },
                  ]}
                  onChange={(v) => onCeilings({ ...ceilings, full_trust_chat: v })}
                  aria-labelledby={`${idp}-chat-l`}
                  disabled={!chatOn || busy}
                />
              </OptRow>
              <OptRow
                label={<label htmlFor={`${idp}-trust`}>Highest session trust</label>}
                labelId={`${idp}-trust-l`}
                tag={
                  <InfoTip label="Highest session trust">
                    Caps the trust it can start a session at, or raise one to, while it may <b>control</b> or{" "}
                    <b>administer</b>. Anything below Full access also keeps it out of the agent's terminal.
                  </InfoTip>
                }
                help={controlOn ? undefined : "Only matters at control or administer."}
              >
                <Select
                  id={`${idp}-trust`}
                  value={ceilings.max_trust ?? ""}
                  options={TRUST_OPTIONS}
                  onChange={(v) => onCeilings({ ...ceilings, max_trust: v || undefined })}
                  disabled={!controlOn || busy}
                />
              </OptRow>
              <OptRow
                label="Fingerprint"
                help={
                  <span className={styles.fpHelp}>
                    Compare with <code>mads device</code> on {row.name}.
                  </span>
                }
              >
                <FingerprintGroups fingerprint={row.fingerprint} />
              </OptRow>
            </>
          ) : (
            <OptRow label="Not paired" help={`${row.name} was added before pairing: no key is pinned for it, so it can't use mads here.`}>
              <span />
            </OptRow>
          )}
          <div className={styles.detailFoot}>
            <span>{row.pairing?.paired_at ? `Paired ${dateTime(row.pairing.paired_at)}` : `Added ${dateTime(row.created_at)}`}</span>
            <span className={styles.spacer} />
            <Button size="sm" onClick={onRemove} id={`${idp}-remove`} disabled={!row.fingerprint}>
              Remove device…
            </Button>
          </div>
        </SettingsCard>
      </div>
      <div>
        <div className={styles.detailTitle}>Activity</div>
        <SettingsCard className={styles.detailCard}>
          {row.fingerprint ? (
            <ActivityList device={row.fingerprint} rows={rows} compact limit={14} />
          ) : (
            <p className={styles.activityEmpty}>Nothing yet.</p>
          )}
        </SettingsCard>
      </div>
    </div>
  );
}
