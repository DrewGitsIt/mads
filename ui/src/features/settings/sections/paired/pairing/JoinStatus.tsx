import { useEffect, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog, DialogText } from "../../../../../components/Dialog/Dialog";
import { CheckCode } from "../../../../../components/CheckCode/CheckCode";
import { devicesApi, devicesStore, type DeviceRow, type JoinStatus as Status } from "../../../../../router/devices";
import { adoptionLine } from "../network";
import { GrantLine, JoinedAnswer } from "./JoinedAnswer";
import { useArmed } from "./useArmed";
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
 * sessions, and what the host may do here (starts at "not use"). A found
 * device not vouched for by Tailscale asks "Did it show this number?"
 * first; a join that made the network this device's mesh network says so.
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
  // "Yes, the same number" waits a moment after it appears, with "No" focused.
  const confirmKey = status.state === "approved" && status.confirm === "needed" ? `${status.id}:${status.check_code}` : null;
  const yesArmed = useArmed(confirmKey);

  // "Yes, the same number" pins the host; "No, it was different" pins
  // nothing and tells the host to undo its side.
  const confirm = async (match: boolean) => {
    setBusy(true);
    setError(undefined);
    try {
      setStatus(await devicesApi.confirmJoin(status.id, match));
      void devicesStore.refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

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

  // A found device approved, and this device's own WhoIs didn't vouch for
  // it: nothing is pinned until its user says the number matched.
  if (status.state === "approved" && status.confirm === "needed" && status.check_code) {
    return (
      <Dialog
        key="confirm"
        title={`${hostName} approved`}
        onDismiss={onClose}
        actions={
          <>
            <Button variant="ghost" disabled={busy} onClick={() => void confirm(false)} data-autofocus="">
              No, it was different
            </Button>
            <span className={styles.arming} data-armed={yesArmed || undefined} key={confirmKey}>
              <Button variant="primary" disabled={busy || !yesArmed} onClick={() => yesArmed && void confirm(true)}>
                Yes, the same number
              </Button>
            </span>
          </>
        }
      >
        <CheckCode code={status.check_code}>
          <b>{hostName} approved.</b> Did it show this number? Only then is {hostName} added here. A different number means
          another device answered.
        </CheckCode>
        {error && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
      </Dialog>
    );
  }

  if (status.state === "approved" && status.confirm === "mismatch") {
    return (
      <Dialog
        title={`${hostName} wasn't added`}
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
            Nothing was added here, and {hostName} was told to undo its side. To be sure you pair with the right device, use a
            pairing code from {hostName}.
          </DialogText>
        </div>
      </Dialog>
    );
  }

  const adopted = adoptionLine(status, host);
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
        <GrantLine status={status} host={host} />
      </div>
      {adopted && (
        <p className={status.adopted_network ? styles.help : styles.raise} data-adoption="">
          {adopted}
        </p>
      )}
      <JoinedAnswer status={status} selfName={selfName} host={host} rows={rows} />
    </Dialog>
  );
}
