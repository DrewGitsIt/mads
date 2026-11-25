import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "../../../components/Button/Button";
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
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { DeviceActivity } from "./paired/DeviceActivity";
import { DeviceTable } from "./paired/DeviceTable";
import { IntroduceDialog, introduceTargets } from "./paired/IntroduceDialog";
import { MeshEmptyState } from "./paired/MeshEmptyState";
import { MeshNetworkCard } from "./paired/MeshNetworkCard";
import { hostWord, hostsSessions, levelOf, needsConfirm, VERB } from "./paired/model";
import { rowProblem, TAILSCALE_ID, tailscaleHere } from "./paired/network";
import { chooseNetwork, MeshSetup, type SetupStep } from "./paired/setup/MeshSetup";
import { RaiseDialog } from "./paired/RaiseDialog";
import { RemoveDeviceDialog } from "./paired/RemoveDeviceDialog";
import { ThisDeviceBar } from "./paired/ThisDeviceBar";
import { CopyCode } from "./paired/pairing/CopyCode";
import { JoinStatus } from "./paired/pairing/JoinStatus";
import { holdPairPromptOpen, usePairOutcome } from "./paired/pairing/PairRequestWatcher";
import { PasteCode } from "./paired/pairing/PasteCode";
import styles from "./paired/PairedDevices.module.css";

/** How long a pairing's answer shows under the bar. */
const OUTCOME_MS = 12_000;

type DialogState =
  | { type: "raise"; row: DeviceRow; level: Level }
  | { type: "remove"; row: DeviceRow }
  | { type: "introduce" }
  | { type: "copy" }
  | { type: "paste"; text?: string }
  | { type: "join"; status: JoinState }
  | { type: "setup"; step: SetupStep; change?: boolean; justSet?: boolean };

/**
 * Settings → Paired devices (docs/MESH-AUTH-DESIGN.md D9, the approved
 * docs/mockups/paired-devices-v3.html; the mesh network from
 * docs/MESH-NETWORK-DESIGN.md U1–U4 and network-reachability-v4.html):
 * this device, its mesh network (or, before one is chosen, the Set up mesh
 * empty state), what other devices may do with mads on this one, what they
 * let this one do, whose sessions show here, and pairing. Every change
 * saves at once; raising a device to control or administer asks first.
 */
export function PairedDevicesSection() {
  const devices = usePairedDevices();
  const self = useSelfDevice();
  const rows = devices.value ?? [];
  const host = hostWord(self.value);
  const selfName = self.value?.name ?? "This device";
  // undefined: untouched, so the first row with a connection problem
  // opens by itself (its Troubleshoot connection with it).
  const [openState, setOpenId] = useState<string | null | undefined>(undefined);
  const openId = openState === undefined ? (rows.find((r) => hostsSessions(r) && rowProblem(r))?.id ?? null) : openState;
  const [settingUp, setSettingUp] = useState(false);
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [busy, setBusy] = useState<ReadonlySet<string>>(() => new Set());
  const [toast, setToast] = useState<{ tone: ToastTone; title: string; description?: string } | null>(null);

  // On screen here, a pairing request opens its prompt by itself, and its
  // answer shows under the bar rather than as a toast over it (Q6).
  useEffect(() => holdPairPromptOpen(), []);
  const outcome = usePairOutcome();
  const [outcomeSeen, setOutcomeSeen] = useState(0);
  const showOutcome = outcome && outcome.at !== outcomeSeen && Date.now() - outcome.at < OUTCOME_MS ? outcome : undefined;
  useEffect(() => {
    if (!showOutcome) return;
    const t = setTimeout(() => setOutcomeSeen(showOutcome.at), OUTCOME_MS - (Date.now() - showOutcome.at));
    return () => clearTimeout(t);
  }, [showOutcome]);

  // Opening the section checks every pairing at once (F6 R3): the answer
  // is the rows, with whether each device still has this one paired. An
  // older Router has no check; the rows stand as read.
  useEffect(() => {
    let live = true;
    devicesApi.check().then(
      (r) => live && Array.isArray(r?.devices) && devicesStore.set(r.devices),
      () => {},
    );
    return () => {
      live = false;
    };
  }, []);

  // A pairing code pasted anywhere on the section (not into a field)
  // opens Paste a pairing code with it (P3). With nothing focused, the
  // section has to be on screen.
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (dialog) return;
    const onPaste = (e: ClipboardEvent) => {
      const text = e.clipboardData?.getData("text/plain").trim() ?? "";
      if (!/^mads-pair-\d+:/.test(text) || !panel.current) return;
      const t = e.target instanceof Element ? e.target : null;
      if (t?.closest("input, textarea, [contenteditable]:not([contenteditable='false'])")) return;
      if (!t || !panel.current.contains(t)) {
        if (t && t !== document.body) return;
        const r = panel.current.getBoundingClientRect();
        if (r.bottom <= 0 || r.top >= window.innerHeight) return;
      }
      e.preventDefault();
      setDialog({ type: "paste", text });
    };
    document.addEventListener("paste", onPaste);
    return () => document.removeEventListener("paste", onPaste);
  }, [dialog]);

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
  const mesh = self.value?.mesh;
  // An older Router has no mesh: it reads as set up (its tailnet), as before.
  const configured = self.value ? (mesh ? mesh.configured : true) : true;
  const showToast = useCallback((t: { tone: ToastTone; title: string; description?: string }) => setToast(t), []);

  // Re-pair (F6 R4): Add devices when the device is in the found list
  // now, else this device's pairing code.
  const repair = (row: DeviceRow) =>
    void write(row, `re-pair ${row.name}`, async () => {
      const found = configured ? await devicesApi.discovered().catch(() => undefined) : undefined;
      const there = found?.devices.some((d) => d.fingerprint === row.fingerprint);
      setDialog(there ? { type: "setup", step: "add" } : { type: "copy" });
    });

  // Set up with Tailscale: one click sets the mesh network, then Add devices.
  const setUpTailscale = async () => {
    setSettingUp(true);
    try {
      await chooseNetwork(TAILSCALE_ID);
      setDialog({ type: "setup", step: "add", justSet: true });
    } catch (err) {
      setToast({ tone: "bad", title: "Couldn't set up Tailscale", description: err instanceof Error ? err.message : String(err) });
    } finally {
      setSettingUp(false);
    }
  };
  const startSetup = () => setDialog({ type: "setup", step: tailscaleHere(mesh) ? "quick" : "choose" });

  return (
    <div className={styles.panel} ref={panel}>
      <ThisDeviceBar
        self={self.value}
        canIntroduce={canIntroduce}
        onIntroduce={() => setDialog({ type: "introduce" })}
        onPair={() => setDialog({ type: "copy" })}
        onAdd={() => setDialog({ type: "setup", step: "add" })}
        configured={Boolean(self.value) && configured}
      />
      {showOutcome && (
        <p className={styles.outcome} role="status" data-pair-outcome={showOutcome.approved ? "added" : "declined"}>
          {showOutcome.approved && (
            <span className={styles.outcomeMark} aria-hidden="true">
              ✓
            </span>
          )}
          <span className={styles.outcomeText}>
            {showOutcome.approved ? (
              <>
                <b>{showOutcome.name}</b> added. It may {VERB[showOutcome.level ?? "none"]} mads on this {host}.
              </>
            ) : (
              <>Declined: {showOutcome.name} wasn't added.</>
            )}
          </span>
          <Button variant="ghost" size="sm" onClick={() => setOutcomeSeen(showOutcome.at)}>
            Dismiss
          </Button>
        </p>
      )}
      {devices.error && !devices.value && (
        <p className={styles.error} role="alert">
          Couldn't read the paired devices: {devices.error}
        </p>
      )}
      {self.value && !configured && (
        <MeshEmptyState
          mesh={mesh}
          busy={settingUp}
          onTailscale={() => void setUpTailscale()}
          onSetUp={() => setDialog({ type: "setup", step: "choose" })}
          onCode={() => setDialog({ type: "paste" })}
        />
      )}
      {self.value && configured && mesh && (
        <MeshNetworkCard self={self.value} host={host} onChange={() => setDialog({ type: "setup", step: "choose", change: true })} onToast={showToast} />
      )}
      {devices.loaded && rows.length === 0 && !devices.error ? (
        configured && (
          <SettingsCard>
            <SettingRow
              stacked
              label="No devices yet"
              help={
                <>
                  Use <b>Add devices</b> above. Each one is approved on the device itself.
                </>
              }
            />
          </SettingsCard>
        )
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
            onRepair={repair}
            busy={busy}
            mesh={mesh}
            onPaste={() => setDialog({ type: "paste" })}
            onToast={showToast}
          />
        )
      )}
      {(configured || rows.length > 0) && <DeviceActivity rows={rows} />}

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
      {dialog?.type === "copy" && (
        <CopyCode host={host} onClose={close} onSetUp={startSetup} configured={configured} onPaste={() => setDialog({ type: "paste" })} />
      )}
      {dialog?.type === "paste" && (
        <PasteCode
          self={self.value}
          rows={rows}
          initial={dialog.text}
          onCancel={close}
          onJoined={(status) => setDialog({ type: "join", status })}
          onUpdated={(name) => setToast({ tone: "ok", title: `Updated ${name}`, description: `This ${host} will look for it at the addresses in the code.` })}
        />
      )}
      {dialog?.type === "setup" && (
        <MeshSetup
          self={self.value}
          rows={rows}
          host={host}
          step={dialog.step}
          change={dialog.change}
          justSet={dialog.justSet}
          onClose={close}
          onUseCode={() => setDialog({ type: "copy" })}
          onChanged={(network) => setToast({ tone: "ok", title: "Mesh network changed", description: `This ${host} now uses ${network}.` })}
        />
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
            void selfStore.refresh();
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
