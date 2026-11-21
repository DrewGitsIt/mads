import { useState } from "react";
import { Button } from "../../../../components/Button/Button";
import { Chip } from "../../../../components/Chip/Chip";
import { Dialog, DialogSection, DialogText, dialogStyles } from "../../../../components/Dialog/Dialog";
import { devicesApi, devicesStore, type DeviceRow, type RemoveResult } from "../../../../router/devices";
import { hostsSessions, isViewed, shortFingerprint, VERB } from "./model";
import type { Level } from "../../../../router/devices";
import styles from "./pairing/Pairing.module.css";

export interface RemoveDeviceDialogProps {
  row: DeviceRow;
  rows: DeviceRow[];
  selfName: string;
  host: string;
  onClose: () => void;
}

/** The other hosts this device knows: where it administers, and where it doesn't. */
export function knownHosts(row: DeviceRow, rows: DeviceRow[]): { admin: DeviceRow[]; not: DeviceRow[] } {
  const hosts = rows.filter((r) => r.id !== row.id && r.fingerprint && hostsSessions(r));
  return {
    admin: hosts.filter((r) => r.remote?.as_of && r.remote.level === "admin"),
    not: hosts.filter((r) => !(r.remote?.as_of && r.remote.level === "admin")),
  };
}

type HostOutcome = RemoveResult["hosts"][number];

/** The devices `row` introduced here: their pairing names it as the introducer (its device actor ends in its short fingerprint). */
export function introducedBy(row: DeviceRow, rows: DeviceRow[]): DeviceRow[] {
  if (!row.fingerprint) return [];
  const suffix = `/${shortFingerprint(row.fingerprint)}`;
  return rows.filter(
    (r) => r.id !== row.id && r.pairing?.how === "introduction" && r.pairing.paired_by.startsWith("device:") && r.pairing.paired_by.endsWith(suffix),
  );
}

/**
 * Remove device…: here at once, then on every other host this device
 * administers, each with its own result (Removed; Offline or Failed, with
 * Retry; no longer an admin). Hosts it can't manage are listed, and hosts
 * it has never seen can't be checked.
 */
export function RemoveDeviceDialog({ row, rows, selfName, host, onClose }: RemoveDeviceDialogProps) {
  const [{ admin, not }] = useState(() => knownHosts(row, rows));
  const [results, setResults] = useState<Map<string, HostOutcome> | null>(null);
  const [cannot, setCannot] = useState<RemoveResult["cannot_manage"]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [introduced] = useState(() => introducedBy(row, rows));
  // On by default: a device that goes keeps nothing here, and neither
  // should the devices it vouched for, unless you keep them on purpose.
  const [alsoIntroduced, setAlsoIntroduced] = useState(true);
  const [alsoRemoved, setAlsoRemoved] = useState<RemoveResult["also_removed"]>([]);
  const [error, setError] = useState<string>();
  const dn = row.name;
  const fp = row.fingerprint!;

  // The option is offered whenever there could be introductions to take
  // with it: ones it made here (listed), or on hosts this device
  // administers (known only once they answer).
  const offerAlso = introduced.length > 0 || admin.length > 0;

  const remove = async (hosts?: string[], also = offerAlso && alsoIntroduced) => {
    setBusy(hosts ? hosts[0] : "all");
    setError(undefined);
    try {
      const out = await devicesApi.removeEverywhere(fp, { hosts, alsoIntroduced: also });
      setResults((cur) => {
        const next = new Map(cur ?? []);
        for (const h of out.hosts) next.set(h.fingerprint, h);
        return next;
      });
      if (!hosts) {
        setCannot(out.cannot_manage ?? []);
        setAlsoRemoved(out.also_removed ?? []);
      }
      void devicesStore.refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };

  const neverSeen = (
    <p className={styles.note}>
      Devices this {host} has never seen can't be checked. If {dn} was paired anywhere else, remove it there too.
    </p>
  );
  const notMine = (name: string, key: string, level?: string) => (
    <li className={styles.item} key={key}>
      <span className={styles.itemName}>{name}</span>
      <Chip tone="neutral">Not yours to manage</Chip>
      <span className={styles.itemHelp}>
        You're not an admin there{level ? ` (it lets this ${host} ${VERB[level as Level] ?? level})` : ""}. Remove it on {name}.
      </span>
    </li>
  );

  if (!results) {
    return (
      <Dialog
        alert
        title={`Remove ${dn}?`}
        onDismiss={onClose}
        actions={
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" className={dialogStyles.danger} disabled={busy !== null} onClick={() => void remove()}>
              {busy ? "Removing…" : "Remove device"}
            </Button>
          </>
        }
      >
        <DialogText>
          <p>
            {dn} is removed from this {host} now: it can't use mads here from its next request
            {isViewed(row) ? ", and its sessions stop showing here" : ""}.
          </p>
        </DialogText>
        <DialogSection title="Where it's removed">
          <ul className={styles.list}>
            <li className={styles.item}>
              <span className={styles.itemName}>
                {selfName} <span>(this device)</span>
              </span>
              <Chip tone="neutral">Here</Chip>
            </li>
            {admin.map((h) => (
              <li className={styles.item} key={h.id}>
                <span className={styles.itemName}>{h.name}</span>
                <Chip tone="info">You administer it</Chip>
              </li>
            ))}
            {not.map((h) => notMine(h.name, h.id))}
          </ul>
        </DialogSection>
        {offerAlso && (
          <DialogSection title="Devices it introduced">
            <label className={styles.check}>
              <input type="checkbox" checked={alsoIntroduced} onChange={(e) => setAlsoIntroduced(e.target.checked)} />
              <span>
                {introduced.length > 0 ? (
                  <>
                    Also remove the {introduced.length === 1 ? "device" : `${introduced.length} devices`} it introduced here:{" "}
                    <b>{introduced.map((r) => r.name).join(", ")}</b>
                    {admin.length > 0 && ", and any it introduced on other devices you manage"}
                  </>
                ) : (
                  <>Also remove any devices it introduced on other devices you manage</>
                )}
                <span className={styles.itemHelp}>
                  Here and on other devices this one administers. {dn} vouched for them; left on, they keep their access.
                </span>
              </span>
            </label>
          </DialogSection>
        )}
        {neverSeen}
        {error && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
      </Dialog>
    );
  }

  const outcomes = [...results.values()];
  const cannotRows = cannot.length > 0 ? cannot : not.map((h) => ({ name: h.name, fingerprint: h.fingerprint!, level: undefined }));
  return (
    <Dialog
      key="results"
      title={`${dn} removed from this ${host}`}
      onDismiss={onClose}
      actions={
        <Button variant="primary" onClick={onClose} data-autofocus="">
          Done
        </Button>
      }
    >
      <ul className={styles.list} aria-label="Results by device">
        <li className={styles.item}>
          <span className={styles.itemName}>
            {selfName} <span>(this device)</span>
          </span>
          <Chip tone="ok">Removed</Chip>
          {(alsoRemoved ?? []).length > 0 && (
            <span className={styles.itemHelp}>With the devices it introduced: {(alsoRemoved ?? []).map((d) => d.name).join(", ")}.</span>
          )}
        </li>
        {outcomes.map((h) => (
          <li className={styles.item} key={h.fingerprint} data-remove-host={h.name} data-result={h.result}>
            <span className={styles.itemName}>{h.name}</span>
            {h.result === "removed" ? (
              <Chip tone="ok">Removed</Chip>
            ) : h.result === "skipped" ? (
              <Chip tone="neutral">Skipped</Chip>
            ) : h.result === "not_admin" ? (
              <Chip tone="neutral">Not an admin there</Chip>
            ) : (
              <span className={styles.result}>
                <Chip tone={h.result === "offline" ? "warn" : "bad"}>{h.result === "offline" ? "Offline" : "Failed"}</Chip>
                <Button variant="ghost" size="sm" disabled={busy !== null} onClick={() => void remove([h.fingerprint])}>
                  {busy === h.fingerprint ? "Retrying…" : "Retry"}
                </Button>
              </span>
            )}
            {h.result === "removed" && (h.introduced_by_this?.length ?? 0) > 0 && (
              <span className={styles.itemHelp} data-introduced-there="">
                It had introduced {h.introduced_by_this!.map((d) => d.name).join(", ")} there
                {(h.also_removed ?? 0) > 0
                  ? `; ${h.also_removed === h.introduced_by_this!.length ? (h.also_removed === 1 ? "removed" : "all removed") : `${h.also_removed} removed`} with it.`
                  : `, which ${h.introduced_by_this!.length === 1 ? "stays" : "stay"}.`}
              </span>
            )}
            {h.result === "removed" && (h.introduced_by_this?.length ?? 0) > 0 && !(h.also_removed ?? 0) && (
              <span className={styles.result}>
                <Button variant="ghost" size="sm" disabled={busy !== null} onClick={() => void remove([h.fingerprint], true)}>
                  {busy === h.fingerprint ? "Removing…" : "Remove them too"}
                </Button>
              </span>
            )}
            {h.result === "removed" && !h.introduced_by_this?.length && (h.also_removed ?? 0) > 0 && (
              <span className={styles.itemHelp}>
                With {h.also_removed} {h.also_removed === 1 ? "device" : "devices"} it introduced there.
              </span>
            )}
            {h.result === "skipped" && <span className={styles.itemHelp}>{h.error || "Not tried."}</span>}
            {h.result === "offline" && <span className={styles.itemHelp}>Not reached. Retry when {h.name} is back.</span>}
            {h.result === "failed" && (
              <span className={styles.itemHelp}>
                {h.name} answered with an error and still has it paired{h.error ? `: ${h.error}` : "."}
              </span>
            )}
            {h.result === "not_admin" && (
              <span className={styles.itemHelp}>{h.name} no longer lets this {host} administer it. Remove it on {h.name}.</span>
            )}
          </li>
        ))}
        {cannotRows.map((h) => notMine(h.name, h.fingerprint, h.level))}
      </ul>
      {neverSeen}
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </Dialog>
  );
}

