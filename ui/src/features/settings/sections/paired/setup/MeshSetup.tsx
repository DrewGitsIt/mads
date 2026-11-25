import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog } from "../../../../../components/Dialog/Dialog";
import { SettingTag } from "../../../SettingRow";
import { devicesApi, devicesStore, pairRequestsStore, selfStore, useLive, type DeviceRow, type SelfDevice } from "../../../../../router/devices";
import { VERB } from "../model";
import { pendingRequests, usePairOutcome } from "../pairing/PairRequestWatcher";
import { markNetworkChanged, choices, chosenNetwork, daysUntil, shortDate, tailscaleHere, TAILSCALE_ADMIN, TAILSCALE_ID, wordFor } from "../network";
import { NetIcon } from "../NetIcon";
import { AddDevices } from "./AddDevices";
import { NetworkChoice } from "./NetworkChoice";
import { NoSharedNetwork } from "./NoSharedNetwork";
import styles from "../Mesh.module.css";

export type SetupStep = "quick" | "choose" | "none" | "add";

/** How long Add devices stays open, untouched, after this device approved one (P4). */
export const AUTO_CLOSE_MS = 5_000;

export interface MeshSetupProps {
  self: SelfDevice | undefined;
  rows: DeviceRow[];
  host: string;
  /** Where it opens: the quick Tailscale card, the full list, or Add devices. */
  step: SetupStep;
  /** Mesh network → Change…: the list alone, saved in place. */
  change?: boolean;
  /** The network was just chosen (Set up with Tailscale): Add devices says so. */
  justSet?: boolean;
  onClose: () => void;
  /** Use a pairing code instead. */
  onUseCode: () => void;
  /** After Change… saves, for the toast. */
  onChanged?: (network: string) => void;
}

/** Saves the mesh network (one, the MVP), answering this device as it is now. */
export async function chooseNetwork(id: string): Promise<SelfDevice> {
  const s = await devicesApi.setMesh({ networks: [id] });
  markNetworkChanged();
  selfStore.set(s);
  return s;
}

/** Plain words for a refused choice (PUT /devices/settings). */
function chooseError(err: unknown): string {
  const code = (err as { code?: string }).code;
  switch (code) {
    case "gateway_unknown":
      return "mads can't tell this network apart from look-alike networks yet (it hasn't talked to the router). Try again in a moment, or use Tailscale.";
    case "unknown_network":
      return "This device isn't on that network any more. Check again.";
    case "one_network":
      return "Choose one network.";
    default:
      return err instanceof Error ? err.message : String(err);
  }
}

/**
 * Set up mesh (U1, "Fewer clicks"): two steps, Network then Add devices.
 * With Tailscale here, step 1 is one card ("Use Tailscale?"); Other
 * options shows every network this device is on, plus None of these (the
 * network-in-common tips). Choosing goes straight to Add your devices.
 * Change… is step 1 alone, saved in place. The steps bar shows only when
 * step 1 is in this dialog. Once this device approves a device, Add
 * devices says so and closes itself after AUTO_CLOSE_MS if nothing in it
 * was touched and no other request waits (P4).
 */
export function MeshSetup({ self, rows, host, step: initial, change, justSet: initialJustSet, onClose, onUseCode, onChanged }: MeshSetupProps) {
  const mesh = self?.mesh;
  const ts = tailscaleHere(mesh);
  const [step, setStep] = useState<SetupStep>(initial === "quick" && !ts ? "choose" : initial);
  // Never preselect a shared network: choosing one is the user's call.
  const [pick, setPickState] = useState<string | undefined>(() => chosenNetwork(mesh)?.id ?? ts?.id ?? choices(mesh).find((n) => !n.shared)?.id);
  const [justSet, setJustSet] = useState(Boolean(initialJustSet));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const startRows = useRef(rows.length);
  // A new pick clears the last pick's error (a LAN's "router unknown").
  const setPick = (id: string | undefined) => {
    setPickState(id);
    setError(undefined);
  };
  const [addedHere, setAddedHere] = useState(false);
  // Step 1 was in this dialog (not skipped by opening at Add devices).
  const [stepOne, setStepOne] = useState(initial !== "add");
  const list = choices(mesh);
  // A request this device approved while Add devices is open.
  const openedAt = useRef(Date.now());
  const outcome = usePairOutcome();
  const approvedHere = outcome?.approved && outcome.at >= openedAt.current ? outcome : undefined;
  const waiting = pendingRequests(useLive(pairRequestsStore).value).length > 0;
  const [touched, setTouched] = useState(false);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (step !== "add" || !approvedHere || touched || waiting) return;
    const t = setTimeout(() => {
      void devicesStore.refresh();
      closeRef.current();
    }, AUTO_CLOSE_MS);
    return () => clearTimeout(t);
  }, [step, approvedHere, touched, waiting]);

  const save = async (id: string) => {
    setSaving(true);
    setError(undefined);
    try {
      const s = await chooseNetwork(id);
      if (change) {
        onChanged?.(wordFor(chosenNetwork(s.mesh)));
        onClose();
        return;
      }
      setJustSet(true);
      setStepOne(true);
      setStep("add");
    } catch (err) {
      setError(chooseError(err));
    } finally {
      setSaving(false);
    }
  };

  const stepsBar =
    change || step === "none" || !stepOne ? null : (
      <ol className={styles.steps} aria-label={`Step ${step === "add" ? 2 : 1} of 2`}>
        <li aria-current={step !== "add" ? "step" : undefined} data-done={step === "add" || undefined}>
          1 Network
        </li>
        <li className={styles.stepRule} aria-hidden="true" />
        <li aria-current={step === "add" ? "step" : undefined}>2 Add devices</li>
      </ol>
    );
  const errorLine = error && (
    <p className={styles.error} role="alert">
      {error}
    </p>
  );

  if (step === "quick" && ts) {
    const expiry = mesh?.tailscale && !mesh.tailscale.key_expiry_off ? mesh.tailscale.key_expiry : undefined;
    return (
      <Dialog
        title="Use Tailscale?"
        className={styles.setupDialog}
        onDismiss={onClose}
        actions={
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" disabled={saving} onClick={() => void save(TAILSCALE_ID)} data-autofocus="">
              {saving ? "Setting up…" : "Set up"}
            </Button>
          </>
        }
      >
        {stepsBar}
        <div className={styles.quick}>
          <NetIcon icon="overlay" />
          <div>
            <div className={styles.choiceTitle}>
              Tailscale <SettingTag tone="ok">Recommended</SettingTag>
            </div>
            <div className={styles.meaning}>
              Your devices reach each other from anywhere they have internet. Only devices on your tailnet can try to connect, and
              only devices you add get in.
            </div>
          </div>
        </div>
        {expiry && (
          <p className={styles.note}>
            Tailscale will sign this {host} out on <b>{shortDate(expiry)}</b> unless you turn off key expiry for it. Do that for
            machines you reach remotely:{" "}
            <a href={TAILSCALE_ADMIN} target="_blank" rel="noreferrer">
              open it in the Tailscale admin console
            </a>
            .
          </p>
        )}
        {errorLine}
        <button type="button" className={styles.linkButton} onClick={() => setStep("choose")}>
          Other options
        </button>
      </Dialog>
    );
  }

  if (step === "choose" || step === "quick") {
    const ids = list.map((n) => n.id);
    const onKey = (e: KeyboardEvent<HTMLButtonElement>) => {
      const d = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
      if (!d || ids.length === 0) return;
      e.preventDefault();
      const i = (ids.indexOf(pick ?? "") + d + ids.length) % ids.length;
      setPick(ids[i]);
      (e.currentTarget.parentElement?.querySelectorAll<HTMLElement>('[role="radio"]')[i])?.focus();
    };
    return (
      <Dialog
        title={change ? "Mesh network" : "How will your devices reach each other?"}
        className={styles.setupDialog}
        onDismiss={onClose}
        actions={
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" disabled={!pick || saving || !ids.includes(pick)} onClick={() => pick && void save(pick)}>
              {change ? "Save" : "Continue"}
            </Button>
          </>
        }
      >
        {stepsBar}
        <p className={styles.lede}>
          mads connects your devices over a network they share. Pick the one this {host} uses. Your other devices pick the same one.
        </p>
        <div className={styles.choices} role="radiogroup" aria-label={`Networks this ${host} is on`}>
          {list.map((n) => (
            <NetworkChoice key={n.id} network={n} checked={pick === n.id} onPick={() => setPick(n.id)} onKeyDown={onKey} />
          ))}
        </div>
        <button type="button" className={`${styles.choice} ${styles.choiceNone}`} onClick={() => setStep("none")} data-network="none">
          <span className={styles.mark} aria-hidden="true" />
          <span>
            <span className={styles.choiceTitle}>None of these</span>
            <span className={styles.meaning}>My devices aren't on the same network, or I'm not sure.</span>
          </span>
        </button>
        <p className={styles.note}>
          Devices only connect over a network they share. <b>To use mads away from home, pick a VPN such as Tailscale:</b> it's one
          network wherever your devices are.
        </p>
        {errorLine}
      </Dialog>
    );
  }

  if (step === "none") {
    return (
      <Dialog
        title="Give your devices a network in common"
        className={styles.setupDialog}
        onDismiss={onClose}
        actions={
          <>
            <Button variant="ghost" onClick={() => setStep("choose")}>
              Back
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                void selfStore.refresh();
                setStep(ts && !change ? "quick" : "choose");
              }}
            >
              Check again
            </Button>
          </>
        }
      >
        <NoSharedNetwork />
      </Dialog>
    );
  }

  const added = addedHere || Boolean(approvedHere) || rows.length > startRows.current;
  const net = chosenNetwork(mesh);
  return (
    <Dialog
      title="Add your devices"
      className={styles.setupDialog}
      onDismiss={onClose}
      actions={
        <Button
          variant={added ? "primary" : "ghost"}
          onClick={() => {
            void devicesStore.refresh();
            onClose();
          }}
        >
          {added ? "Done" : "Skip for now"}
        </Button>
      }
    >
      {stepsBar}
      {/* Any click or key in here keeps it open. */}
      <div className={styles.contents} onPointerDownCapture={() => setTouched(true)} onKeyDownCapture={() => setTouched(true)}>
        {approvedHere && (
          <p className={styles.done} role="status" data-approved-here="">
            <span className={styles.doneMark} aria-hidden="true">
              ✓
            </span>
            <span>
              <b>{approvedHere.name}</b> added: it may {VERB[approvedHere.level ?? "none"]} mads on this {host}. Add another, or Done.
            </span>
          </p>
        )}
        <AddDevices self={self} rows={rows} host={host} justSet={justSet} onUseCode={onUseCode} onAdded={() => setAddedHere(true)} />
      </div>
      {justSet && net?.kind === "lan" && self?.platform === "darwin" && (
        <p className={styles.note}>
          macOS may ask whether mads may accept incoming connections. Choose <b>Allow</b>.
        </p>
      )}
      {justSet && net?.id === TAILSCALE_ID && mesh?.tailscale?.key_expiry && !mesh.tailscale.key_expiry_off && (daysUntil(mesh.tailscale.key_expiry) ?? 99) <= 7 && (
        <p className={styles.note}>
          Tailscale signs this {host} out on <b>{shortDate(mesh.tailscale.key_expiry)}</b>.{" "}
          <a href={TAILSCALE_ADMIN} target="_blank" rel="noreferrer">
            Turn off key expiry
          </a>{" "}
          so it stays reachable.
        </p>
      )}
    </Dialog>
  );
}
