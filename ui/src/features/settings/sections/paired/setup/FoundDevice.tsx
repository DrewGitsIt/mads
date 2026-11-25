import { useEffect, useId, useRef } from "react";
import { Button } from "../../../../../components/Button/Button";
import { CheckCode } from "../../../../../components/CheckCode/CheckCode";
import { Chip } from "../../../../../components/Chip/Chip";
import { KindIcon } from "../../../../../components/KindIcon/KindIcon";
import type { DeviceRow, DiscoveredDevice } from "../../../../../router/devices";
import { kindOf, platformLabel } from "../model";
import { adoptionLine, joinFailLine } from "../network";
import { GrantLine, JoinedAnswer } from "../pairing/JoinedAnswer";
import { useFoundJoin } from "./useSetup";
import { useArmed } from "../pairing/useArmed";
import { useRetryWait } from "../pairing/useRetryWait";
import pairingStyles from "../pairing/Pairing.module.css";
import styles from "../Mesh.module.css";

export interface FoundDeviceProps {
  device: DiscoveredDevice;
  selfName: string;
  host: string;
  rows: DeviceRow[];
  /** It was added here (approved, and the number matched or Tailscale vouched). */
  onAdded?: () => void;
  /** This device's Mesh Service hasn't reported where it listens yet: a request waits for it. */
  gettingReady?: boolean;
}

/**
 * A mads device found on the mesh network (U4), and its pairing from Add
 * to done: waiting on its screen (Tailscale vouched, so no number; or the
 * check code to compare there), then "Did it show this number?" when this
 * device's own Tailscale didn't vouch, then what it granted and this
 * device's answer.
 */
export function FoundDevice({ device: d, selfName, host, rows, onAdded, gettingReady }: FoundDeviceProps) {
  const join = useFoundJoin(d.fingerprint);
  const st = join.status;
  const added = st?.state === "approved" && st.confirm !== "needed" && st.confirm !== "mismatch";
  // "Yes, the same number" waits a moment after it appears, with "No" focused.
  const confirmKey = st?.state === "approved" && st.confirm === "needed" ? `${st.id}:${st.check_code}` : null;
  const yesArmed = useArmed(confirmKey);
  const noId = useId();
  useEffect(() => {
    if (confirmKey) document.getElementById(noId)?.focus();
  }, [confirmKey, noId]);
  const addedRef = useRef(onAdded);
  addedRef.current = onAdded;
  useEffect(() => {
    if (added) addedRef.current?.();
  }, [added]);
  const wait = useRetryWait(join.error, join.error?.after ?? 0);
  const name = d.peer_name || d.name;
  const kind = kindOf(d.platform);
  const where = d.network_label || (d.via === "tailscale" ? "Tailscale" : "the local network");

  // Which sides have the pairing (F6 R5); an older Router only says whether a row exists here.
  const pairing = d.pairing ?? (d.paired ? "both" : "none");
  const repair = pairing === "here_only" || pairing === "there_only";
  let right;
  if (pairing === "both" && !st) right = <Chip tone="ok">Paired ✓</Chip>;
  else if (!st) {
    // Re-pair is the same request as Add: the other device's approval replaces its old side.
    right = d.compatible ? (
      <Button variant="primary" size="sm" disabled={join.busy} onClick={() => void join.request()} aria-label={`${repair ? "Re-pair" : "Add"} ${d.name}`}>
        {join.busy ? (gettingReady ? "Getting this device ready…" : "Asking…") : repair ? "Re-pair" : "Add"}
      </Button>
    ) : (
      <Chip tone="warn">Needs a mads update</Chip>
    );
  } else if (st.state === "pending") {
    right = (
      <span className={styles.wait} role="status">
        <span className={styles.spinner} aria-hidden="true" />
        Waiting for {d.name}
      </span>
    );
  } else if (st.state === "declined") right = <Chip tone="bad">Declined</Chip>;
  else if (st.state === "expired") right = <Chip tone="bad">Didn't answer</Chip>;
  else if (st.confirm === "needed") right = <Chip tone="info">Approved</Chip>;
  else if (st.confirm === "mismatch") right = <Chip tone="bad">Not added</Chip>;
  else right = <Chip tone="ok">Added</Chip>;

  let after = null;
  if (join.error) {
    after = (
      <>
        <p className={styles.error} role="alert" data-refusal={join.error.code}>
          {joinFailLine(join.error, d.name, host)}
        </p>
        {join.error.retryable && (
          <div className={styles.actionsRow}>
            <Button size="sm" disabled={join.busy || wait > 0} onClick={() => void join.request()}>
              {wait > 0 ? `Try again in ${wait} s` : "Try again"}
            </Button>
          </div>
        )}
      </>
    );
  } else if (!st && repair && d.compatible) {
    after = (
      <p className={`${styles.help} ${styles.repairWhy}`} data-repair={pairing}>
        {pairing === "here_only" ? `${d.name} no longer has this ${host} paired.` : `${d.name} still has this ${host} from an earlier pairing.`}
      </p>
    );
  } else if (st?.state === "pending" && st.vouched) {
    after = (
      <p className={styles.help}>
        Tailscale confirms {d.name} is one of your devices, so there's no number to compare. Approve it on {d.name}.
      </p>
    );
  } else if (st?.state === "pending" && st.check_code) {
    after = (
      <CheckCode code={st.check_code}>
        Check that {d.name} shows the same number, then approve there. If it shows a different one, decline it there.
      </CheckCode>
    );
  } else if (st?.state === "approved" && st.confirm === "needed" && st.check_code) {
    after = (
      <>
        <CheckCode code={st.check_code}>
          <b>{d.name} approved.</b> Did it show this number? Only then is {d.name} added here. A different number means another
          device answered.
        </CheckCode>
        <div className={styles.actionsRow}>
          <span className={pairingStyles.arming} data-armed={yesArmed || undefined} key={confirmKey}>
            <Button variant="primary" size="sm" disabled={join.busy || !yesArmed} onClick={() => yesArmed && void join.confirm(true)}>
              Yes, the same number
            </Button>
          </span>
          <Button id={noId} variant="ghost" size="sm" disabled={join.busy} onClick={() => void join.confirm(false)}>
            No, it was different
          </Button>
        </div>
      </>
    );
  } else if (st?.state === "approved" && st.confirm === "mismatch") {
    after = (
      <p className={styles.help}>
        Nothing was added here, and {d.name} was told to undo its side. To be sure you pair with the right device, use a pairing
        code from {d.name}.
      </p>
    );
  } else if (st?.state === "approved") {
    const adopted = adoptionLine(st, host);
    after = (
      <>
        <GrantLine status={st} host={host} />
        {adopted && <p className={styles.help}>{adopted}</p>}
        <JoinedAnswer status={st} selfName={selfName} host={host} rows={rows} />
      </>
    );
  } else if (st?.state === "declined") {
    after = <p className={styles.help}>{d.name} declined. Nothing was paired.</p>;
  } else if (st?.state === "expired") {
    after = <p className={styles.help}>Nobody approved on {d.name} in time. Nothing was paired. Add it again to retry.</p>;
  }

  return (
    <li className={styles.foundItem} data-found={d.name}>
      <div className={styles.foundLine}>
        <KindIcon kind={kind} label={platformLabel(d.platform)} />
        <span>
          <span className={styles.foundName}>{name}</span>{" "}
          <span className={styles.foundMeta}>
            {d.platform ? `${platformLabel(d.platform)} · ` : ""}on {where} · <span className={styles.foundFp}>{d.short}</span>
          </span>
        </span>
        <span className={styles.spacer} />
        {right}
      </div>
      {after && <div className={styles.after}>{after}</div>}
    </li>
  );
}
