import { useEffect, useState } from "react";
import { markNetworkChanged } from "../network";
import { Switch } from "../../../../../components/Switch/Switch";
import { devicesApi, devicesStore, type DeviceRow, type JoinStatus, type Level } from "../../../../../router/devices";
import { LevelChoice } from "../LevelChoice";
import { RaiseDialog } from "../RaiseDialog";
import { hostsSessions, isViewed, kindOfRow, levelOf, needsConfirm, raiseLine, VERB } from "../model";
import styles from "./Pairing.module.css";

export interface JoinedAnswerProps {
  status: JoinStatus;
  /** This device's own name and its short word ("this MacBook-Air"). */
  selfName: string;
  host: string;
  rows: DeviceRow[];
}

/** What the host lets this device do, once it approved. */
export function GrantLine({ status, host }: { status: JoinStatus; host: string }) {
  const granted = (status.level || "none") as Level;
  const hostName = status.host.name;
  return (
    <p className={styles.grant} data-join-granted="">
      {granted === "none" ? (
        <>
          {hostName} paired with this {host} but doesn't let it use mads there yet.
        </>
      ) : (
        <>
          {hostName} lets this {host} <b>{VERB[granted]}</b> mads there.
        </>
      )}
    </p>
  );
}

/**
 * The joiner's side once the host approved and is pinned here: this
 * device's own answer (Show sessions,
 * and what the host may do here, starting at "not use"). Shared by the
 * pairing-code dialog and a found device in Add devices. Raising the host
 * to control asks first, as raising a row in the table does (Q8).
 */
export function JoinedAnswer({ status, selfName, host, rows }: JoinedAnswerProps) {
  // Pairing made a network this device's mesh network: the card reads
  // "Getting ready…" while its Mesh Service starts listening there.
  const adopted = status.adopted_network?.id;
  useEffect(() => {
    if (adopted) markNetworkChanged();
  }, [adopted]);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [raising, setRaising] = useState<Level | null>(null);
  const hostName = status.host.name;
  const row = rows.find((r) => r.fingerprint === status.host.fingerprint);
  const granted = (status.level || "none") as Level;
  const back = row ? levelOf(row) : "none";
  const isComputer = row ? kindOfRow(row) === "computer" : true;
  const hosts = row ? hostsSessions(row) : true;
  const viewed = row ? isViewed(row) : false;

  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await f();
      await devicesStore.refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const setLevel = (l: Level, confirmed = false) => {
    if (!row?.fingerprint || l === back) return;
    if (!confirmed && needsConfirm(back, l)) {
      setRaising(l);
      return;
    }
    void run(() => devicesApi.setGrant(row.fingerprint!, l));
  };

  return (
    <>
      {status.error && <p className={styles.error}>{status.error}</p>}
      {hosts && (
        <div className={styles.ask}>
          <div className={styles.showRow}>
            <Switch
              checked={viewed && granted !== "none"}
              disabled={granted === "none" || busy || !row}
              aria-labelledby={`join-show-${status.id}`}
              onChange={(v) => row && void run(() => (v ? devicesApi.view(row) : devicesApi.unview(row.name)))}
            />
            <div className={styles.showText}>
              <span className={styles.showLabel} id={`join-show-${status.id}`} data-disabled={granted === "none" || undefined}>
                Show mads sessions from {hostName}
              </span>
              <span className={styles.showStatus}>
                {granted === "none"
                  ? `Nothing to show until it lets this ${host} view.`
                  : "They appear under All devices. Showing gives it nothing here."}
              </span>
            </div>
          </div>
        </div>
      )}
      <div className={styles.ask}>
        <LevelChoice
          subject={hostName}
          where={`on this ${host}`}
          value={back}
          disabled={busy || !row?.fingerprint}
          onChange={(l) => setLevel(l)}
        />
        <span className={styles.help}>
          Your choice, set here on this {host}. Nothing until you choose.
          {isComputer && back === "none" && (
            <>
              {" "}
              <span className={styles.suggest}>
                Suggested: <b>control</b>
              </span>
            </>
          )}
        </span>
        {back === "control" && <div className={styles.raise}>{raiseLine(hostName, "control", selfName)}</div>}
      </div>
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      {raising && (
        <RaiseDialog
          device={hostName}
          level={raising}
          selfName={selfName}
          host={host}
          onCancel={() => setRaising(null)}
          onConfirm={() => {
            setRaising(null);
            setLevel(raising, true);
          }}
        />
      )}
    </>
  );
}
