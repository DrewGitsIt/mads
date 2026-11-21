import { useCallback, useEffect, useState } from "react";
import { Disclosure } from "../../../components/Disclosure/Disclosure";
import { Switch } from "../../../components/Switch/Switch";
import { Toast, type ToastTone } from "../../../components/Toast/Toast";
import { refreshPeers } from "../../../router/mesh";
import {
  devicesApi,
  devicesStore,
  selfStore,
  usePairedDevices,
  useSelfDevice,
  type DeviceRow,
  type JoinStatus as JoinState,
  type Level,
  type PeerCeilings,
} from "../../../router/devices";
import { SettingRow, SettingTag } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { DeviceActivity } from "./paired/DeviceActivity";
import { DeviceTable } from "./paired/DeviceTable";
import { IntroduceDialog, introduceTargets } from "./paired/IntroduceDialog";
import { cappedLine, hostWord, levelOf, needsConfirm, VERB } from "./paired/model";
import { RaiseDialog } from "./paired/RaiseDialog";
import { RemoveDeviceDialog } from "./paired/RemoveDeviceDialog";
import { ThisDeviceBar } from "./paired/ThisDeviceBar";
import { CopyCode } from "./paired/pairing/CopyCode";
import { JoinStatus } from "./paired/pairing/JoinStatus";
import { PairChooser } from "./paired/pairing/PairChooser";
import { holdPairPromptOpen } from "./paired/pairing/PairRequestWatcher";
import { PasteCode } from "./paired/pairing/PasteCode";
import styles from "./paired/PairedDevices.module.css";

type DialogState =
  | { type: "raise"; row: DeviceRow; level: Level }
  | { type: "remove"; row: DeviceRow }
  | { type: "introduce" }
  | { type: "choose" }
  | { type: "copy" }
  | { type: "paste" }
  | { type: "join"; status: JoinState };

/**
 * Settings → Paired devices (docs/MESH-AUTH-DESIGN.md D9, the approved
 * docs/mockups/paired-devices-v3.html): what other devices may do with mads
 * on this one, what they let this one do, whose sessions show here, and
 * pairing. Every change saves at once; raising a device to control or
 * administer asks first.
 */
export function PairedDevicesSection() {
  const devices = usePairedDevices();
  const self = useSelfDevice();
  const rows = devices.value ?? [];
  const host = hostWord(self.value);
  const selfName = self.value?.name ?? "This device";
  const [openId, setOpenId] = useState<string | null>(null);
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [busy, setBusy] = useState<ReadonlySet<string>>(() => new Set());
  const [toast, setToast] = useState<{ tone: ToastTone; title: string; description?: string } | null>(null);

  // On screen here, a pairing request opens its prompt by itself.
  useEffect(() => holdPairPromptOpen(), []);

  // Esc closes an open row (when nothing else has the key).
  useEffect(() => {
    if (!openId || dialog) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (document.querySelector('[aria-expanded="true"]:not(tr)')) return;
      setOpenId(null);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [openId, dialog]);

  const write = useCallback(async (row: DeviceRow, what: string, f: () => Promise<unknown>) => {
    setBusy((b) => new Set(b).add(row.id));
    try {
      await f();
      await devicesStore.refresh();
    } catch (err) {
      setToast({ tone: "bad", title: `Couldn't ${what}`, description: err instanceof Error ? err.message : String(err) });
    } finally {
      setBusy((b) => {
        const next = new Set(b);
        next.delete(row.id);
        return next;
      });
    }
  }, []);

  const setLevel = (row: DeviceRow, level: Level, confirmed = false) => {
    const from = levelOf(row);
    if (level === from || !row.fingerprint) return;
    if (!confirmed && needsConfirm(from, level)) {
      setDialog({ type: "raise", row, level });
      return;
    }
    void write(row, `change what ${row.name} may do`, () => devicesApi.setGrant(row.fingerprint!, level));
  };

  const setCeilings = (row: DeviceRow, ceilings: PeerCeilings) =>
    void write(row, `change ${row.name}'s limits`, () => devicesApi.setGrant(row.fingerprint!, levelOf(row), ceilings));

  const setShowSessions = (row: DeviceRow, show: boolean) =>
    void write(row, show ? `show ${row.name}'s sessions` : `stop showing ${row.name}'s sessions`, async () => {
      if (show) await devicesApi.view(row);
      else await devicesApi.unview(row.name);
      await refreshPeers();
    });

  const close = useCallback(() => setDialog(null), []);
  const canIntroduce = introduceTargets(rows).length > 0;
  // A device that grants this one administer, held below it by the tailnet policy.
  const capped = rows.filter((r) => r.remote?.tailnet_capped && r.remote.granted === "admin");
  const introduceWhy = capped.map((r) => `${r.name} lets this ${host} ${cappedLine(r)}.`).join(" ");

  return (
    <div className={styles.panel}>
      <ThisDeviceBar
        self={self.value}
        canIntroduce={canIntroduce}
        introduceWhy={introduceWhy}
        onIntroduce={() => setDialog({ type: "introduce" })}
        onPair={() => setDialog({ type: "choose" })}
      />
      {devices.error && !devices.value && (
        <p className={styles.error} role="alert">
          Couldn't read the paired devices: {devices.error}
        </p>
      )}
      {devices.loaded && rows.length === 0 && !devices.error ? (
        <SettingsCard>
          <SettingRow
            stacked
            label="No paired devices"
            help="Pair a device to use mads here from it, or to show its sessions here."
          />
        </SettingsCard>
      ) : (
        rows.length > 0 && (
          <DeviceTable
            rows={rows}
            host={host}
            openId={openId}
            onToggle={setOpenId}
            onLevel={setLevel}
            onCeilings={setCeilings}
            onShowSessions={setShowSessions}
            onRemove={(row) => setDialog({ type: "remove", row })}
            busy={busy}
          />
        )
      )}
      <DeviceActivity rows={rows} />
      <SettingsCard>
        <Disclosure summary="Advanced options">
          <TailnetOnly />
        </Disclosure>
      </SettingsCard>

      {dialog?.type === "raise" && (
        <RaiseDialog
          device={dialog.row.name}
          level={dialog.level}
          selfName={selfName}
          host={host}
          onCancel={close}
          onConfirm={() => {
            setDialog(null);
            setLevel(dialog.row, dialog.level, true);
          }}
        />
      )}
      {dialog?.type === "remove" && (
        <RemoveDeviceDialog
          row={dialog.row}
          rows={rows}
          selfName={selfName}
          host={host}
          onClose={() => {
            setDialog(null);
            setOpenId(null);
            void refreshPeers();
          }}
        />
      )}
      {dialog?.type === "introduce" && (
        <IntroduceDialog
          rows={rows}
          host={host}
          onCancel={close}
          onDone={({ device, to, level }) => {
            setDialog(null);
            setToast({ tone: "ok", title: `Introduced ${device} to ${to}`, description: `It may ${VERB[level]} mads there.` });
          }}
        />
      )}
      {dialog?.type === "choose" && (
        <PairChooser
          host={host}
          onCancel={close}
          onCopy={() => setDialog({ type: "copy" })}
          onPaste={() => setDialog({ type: "paste" })}
        />
      )}
      {dialog?.type === "copy" && <CopyCode host={host} onClose={close} />}
      {dialog?.type === "paste" && (
        <PasteCode self={self.value} rows={rows} onCancel={close} onJoined={(status) => setDialog({ type: "join", status })} />
      )}
      {dialog?.type === "join" && (
        <JoinStatus
          initial={dialog.status}
          selfName={selfName}
          host={host}
          rows={rows}
          onClose={() => {
            setDialog(null);
            void refreshPeers();
          }}
        />
      )}
      {toast && <Toast tone={toast.tone} title={toast.title} description={toast.description} onDismiss={() => setToast(null)} />}
    </div>
  );
}

/** The section's lede, with this device's own short name ("on this pop-os"). */
export function PairedDevicesLede() {
  const host = hostWord(useSelfDevice().value);
  return (
    <>
      What other devices may do with mads on this {host}, and whose sessions show here. Each device sets this for itself.
    </>
  );
}

/** "Only accept devices on my tailnet": this host's own setting, live. */
function TailnetOnly() {
  const self = useSelfDevice().value;
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  const on = self?.require_tailnet ?? true;
  const locked = Boolean(self?.require_tailnet_locked);
  return (
    <SettingRow
      label="Only accept devices on my tailnet"
      htmlFor="tailnet-only"
      tag={on ? undefined : <SettingTag tone="warn">Less safe</SettingTag>}
      help={
        <>
          {locked
            ? "Set by -require-tailnet on this device's Mesh Service, which overrides this switch until it's started without it."
            : on
              ? "A paired device must also be signed in to your tailnet as you."
              : "Any device with a paired key can connect, from any network this one listens on."}
          {error && (
            <>
              {" "}
              <span className={styles.error} role="alert">
                {error}
              </span>
            </>
          )}
        </>
      }
    >
      <Switch
        id="tailnet-only"
        checked={on}
        disabled={!self || locked || saving}
        onChange={(v) => {
          setSaving(true);
          setError(undefined);
          devicesApi
            .setSettings({ require_tailnet: v })
            .then(
              (s) => selfStore.set(s),
              (err: unknown) => setError(err instanceof Error ? err.message : String(err)),
            )
            .finally(() => setSaving(false));
        }}
      />
    </SettingRow>
  );
}
