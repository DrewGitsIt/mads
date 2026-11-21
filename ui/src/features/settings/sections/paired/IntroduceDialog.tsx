import { useState } from "react";
import { Button } from "../../../../components/Button/Button";
import { Dialog, DialogText } from "../../../../components/Dialog/Dialog";
import { Select } from "../../../../components/Select/Select";
import { devicesApi, type DeviceRow, type Level } from "../../../../router/devices";
import { LevelChoice } from "./LevelChoice";
import { kindOfRow, SUGGESTED } from "./model";
import styles from "./pairing/Pairing.module.css";

/** The hosts this device may introduce devices to: those that let it administer them. */
export function introduceTargets(rows: DeviceRow[]): DeviceRow[] {
  return rows.filter((r) => r.fingerprint && r.mesh_url && r.remote?.as_of && r.remote.level === "admin");
}

/** Introducing caps at control: administer is only ever set on the host itself (Q2). */
export const INTRODUCE_LEVELS: Level[] = ["view", "chat", "control"];

export interface IntroduceDialogProps {
  rows: DeviceRow[];
  host: string;
  onDone: (outcome: { device: string; to: string; level: Level }) => void;
  onCancel: () => void;
}

/**
 * Introduce a device: adds a device paired here to a host that lets this
 * one administer it, without pairing there; at most control.
 */
export function IntroduceDialog({ rows, host, onDone, onCancel }: IntroduceDialogProps) {
  const targets = introduceTargets(rows);
  const [to, setTo] = useState(targets[0]?.fingerprint ?? "");
  const candidates = rows.filter((r) => r.fingerprint && r.fingerprint !== to);
  const [device, setDevice] = useState(candidates[0]?.fingerprint ?? "");
  const dev = candidates.find((r) => r.fingerprint === device) ?? candidates[0];
  const [level, setLevel] = useState<Level>(() => (dev ? cap(SUGGESTED[kindOfRow(dev)]) : "view"));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const target = targets.find((t) => t.fingerprint === to);

  if (!target) {
    return (
      <Dialog
        title="Introduce a device"
        onDismiss={onCancel}
        actions={
          <Button onClick={onCancel} data-autofocus="">
            Close
          </Button>
        }
      >
        <DialogText>
          <p>Introducing adds a device you've paired here to another device, without pairing there.</p>
          <p>
            This {host} needs to <b>administer</b> mads on that device first. That's set on that device: Settings → Paired
            devices.
          </p>
        </DialogText>
      </Dialog>
    );
  }

  const introduce = async () => {
    if (!dev) return;
    setBusy(true);
    setError(undefined);
    try {
      await devicesApi.introduce(dev.fingerprint!, target.fingerprint!, level);
      onDone({ device: dev.name, to: target.name, level });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      title="Introduce a device"
      onDismiss={onCancel}
      actions={
        <>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!dev || busy} onClick={() => void introduce()}>
            {busy ? "Introducing…" : "Introduce"}
          </Button>
        </>
      }
    >
      <DialogText>
        Adds a device paired here to {target.name}, without pairing there. {target.name} records it as introduced by this {host}.
      </DialogText>
      <div className={styles.grid2}>
        <div className={styles.field}>
          <label className={styles.label} htmlFor="intro-dev">
            Device
          </label>
          <Select
            id="intro-dev"
            value={dev?.fingerprint ?? ""}
            placeholder="Nothing to introduce"
            options={candidates.map((r) => ({ value: r.fingerprint!, label: r.name }))}
            onChange={(v) => {
              setDevice(v);
              const r = candidates.find((c) => c.fingerprint === v);
              if (r) setLevel(cap(SUGGESTED[kindOfRow(r)]));
            }}
          />
        </div>
        <div className={styles.field}>
          <label className={styles.label} htmlFor="intro-host">
            To
          </label>
          <Select id="intro-host" value={to} options={targets.map((t) => ({ value: t.fingerprint!, label: t.name }))} onChange={setTo} />
          <span className={styles.help}>This {host} administers mads there.</span>
        </div>
      </div>
      {dev && (
        <div className={styles.ask}>
          <LevelChoice subject={dev.name} where={`on ${target.name}`} value={level} onChange={setLevel} levels={INTRODUCE_LEVELS} disabled={busy} />
          <span className={styles.help}>Up to control. Administer is only set on {target.name} itself.</span>
        </div>
      )}
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </Dialog>
  );
}

function cap(l: Level): Level {
  return INTRODUCE_LEVELS.includes(l) ? l : "view";
}
