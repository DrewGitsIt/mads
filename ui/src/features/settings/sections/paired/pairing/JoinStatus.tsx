import { useEffect, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog, DialogText } from "../../../../../components/Dialog/Dialog";
import { Switch } from "../../../../../components/Switch/Switch";
import { devicesApi, devicesStore, type DeviceRow, type JoinStatus as Status, type Level } from "../../../../../router/devices";
import { LevelChoice } from "../LevelChoice";
import { hostsSessions, isViewed, kindOfRow, levelOf, raiseLine, VERB } from "../model";
import styles from "./Pairing.module.css";

export const JOIN_POLL_MS = 2000;
/** The longest wait between polls after failed ones. */
export const JOIN_POLL_MAX_MS = 30_000;

export interface JoinStatusProps {
  initial: Status;
  /** This device's own name and its short word ("this MacBook-Air"). */
  selfName: string;
  host: string;
  rows: DeviceRow[];
  onClose: () => void;
}

/**
 * The joiner's side, after Ask to pair: waiting for the host, then what it
 * granted, and this device's own answer: whether to show the host's
 * sessions, and what the host may do here (starts at "not use").
 */
export function JoinStatus({ initial, selfName, host, rows, onClose }: JoinStatusProps) {
  const [status, setStatus] = useState(initial);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  // Polls failed in a row: each one doubles the wait before the next, up
  // to JOIN_POLL_MAX_MS. Polling goes on until an answer or the code's
  // expiry, so a blip (the Router restarting) doesn't strand the dialog.
  const [failures, setFailures] = useState(0);
  const hostName = status.host.name;

  useEffect(() => {
    if (status.state !== "pending") return;
    const expires = Date.parse(status.expires_at);
    if (failures > 0 && Number.isFinite(expires) && Date.now() >= expires) {
      setStatus({ ...status, state: "expired" });
      return;
    }
    const wait = Math.min(JOIN_POLL_MS * 2 ** failures, JOIN_POLL_MAX_MS);
    let live = true;
    const t = setTimeout(() => {
      devicesApi.joinStatus(status.id).then(
        (s) => {
          if (!live) return;
          setError(undefined);
          setFailures(0);
          setStatus(s);
          if (s.state === "approved") void devicesStore.refresh();
        },
        (err: unknown) => {
          if (!live) return;
          setError(err instanceof Error ? err.message : String(err));
          setFailures((n) => n + 1);
        },
      );
    }, wait);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [status, failures]);

  if (status.state === "pending") {
    return (
      <Dialog
        title={`Waiting for ${hostName}`}
        onDismiss={onClose}
        actions={
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
        }
      >
        <div className={styles.looking} role="status">
          <span className={styles.spinner} aria-hidden="true" />
          Waiting for {hostName} to approve this {host} and choose what it may do there…
        </div>
        {status.self && (
          <p className={styles.help} data-join-self="">
            This device: <span className={styles.meta}>{status.self}</span>. {hostName} will see it in its prompt: check they match.
          </p>
        )}
        {error && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
      </Dialog>
    );
  }

  if (status.state === "declined" || status.state === "expired") {
    return (
      <Dialog
        title={status.state === "declined" ? `${hostName} declined` : `${hostName} didn't answer`}
        onDismiss={onClose}
        actions={
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        }
      >
        <div className={styles.status}>
          <span className={styles.failMark} aria-hidden="true">
            !
          </span>
          <DialogText>
            {status.state === "declined" ? "Nothing was paired." : "Nobody approved in time. Nothing was paired."} To try again,
            ask {hostName} for a new code.
          </DialogText>
        </div>
      </Dialog>
    );
  }

  // Approved: the host's row here, pinned with no grant.
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

  return (
    <Dialog
      key="done"
      title={`Paired with ${hostName}`}
      onDismiss={onClose}
      actions={
        <Button variant="primary" onClick={onClose} data-autofocus="">
          Done
        </Button>
      }
    >
      <div className={styles.status}>
        <span className={styles.doneMark} aria-hidden="true">
          ✓
        </span>
        <p className={styles.grant}>
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
      </div>
      {status.error && <p className={styles.error}>{status.error}</p>}
      {hosts && (
        <div className={styles.ask}>
          <div className={styles.showRow}>
            <Switch
              checked={viewed && granted !== "none"}
              disabled={granted === "none" || busy || !row}
              aria-labelledby="join-show-l"
              onChange={(v) => row && void run(() => (v ? devicesApi.view(row) : devicesApi.unview(row.name)))}
            />
            <div className={styles.showText}>
              <span className={styles.showLabel} id="join-show-l" data-disabled={granted === "none" || undefined}>
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
          onChange={(l) => row?.fingerprint && void run(() => devicesApi.setGrant(row.fingerprint!, l))}
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
    </Dialog>
  );
}
