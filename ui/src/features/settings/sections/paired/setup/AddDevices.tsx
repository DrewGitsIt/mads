import { useEffect, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Disclosure } from "../../../../../components/Disclosure/Disclosure";
import { devicesApi, devicesStore, pairRequestsStore, useLive, type DeviceRow, type DiscoveredDevice, type SelfDevice } from "../../../../../router/devices";
import { cardState, chosenNetwork, discoveryErrorLine, hiddenLine, settling, windowClosedLine, wordFor } from "../network";
import { relTime } from "../model";
import { FoundDevice } from "./FoundDevice";
import { useDiscovered, useSetupWindow } from "./useSetup";
import styles from "../Mesh.module.css";

export interface AddDevicesProps {
  self: SelfDevice | undefined;
  rows: DeviceRow[];
  host: string;
  /** Just chosen: lead with "This <host> is reachable on X". */
  justSet?: boolean;
  onUseCode: () => void;
  /** A found device was added here. */
  onAdded?: () => void;
}

/**
 * Add your devices (U1 step 3, U4): this device is findable while it's
 * open, and the list shows only mads devices whose own setup is open on
 * the same network. Each found device is added (and approved on its own
 * screen) in place. Its first line says what the Mesh network card says
 * (Q3), the help folds away once a device is found, and the spinner only
 * turns while nothing is (Q11).
 */
export function AddDevices({ self, rows, host, justSet, onUseCode, onAdded }: AddDevicesProps) {
  const [win, reopen] = useSetupWindow(true);
  const open = win.state === "open" || win.state === "opening";
  const { list, error } = useDiscovered(open);
  const mesh = self?.mesh;
  const net = chosenNetwork(mesh);
  const word = wordFor(net);
  const selfName = self?.name ?? "This device";

  // Opening Add devices checks every pairing too (F6 R3).
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

  // Everything found while the dialog is open stays listed, so a device
  // mid-pairing doesn't vanish when its own setup closes.
  const [seen, setSeen] = useState<DiscoveredDevice[]>([]);
  useEffect(() => {
    if (!list) return;
    setSeen((prev) => {
      const next = [...prev];
      for (const d of list.devices) {
        const i = next.findIndex((x) => x.fingerprint === d.fingerprint);
        if (i >= 0) next[i] = d;
        else next.push(d);
      }
      return next;
    });
  }, [list]);

  // The window closed itself (abandoned pairings), as the self store says.
  const closedBySelf = win.state === "open" && mesh && !mesh.discoverable_until && mesh.window_closed_reason ? mesh.window_closed_reason : undefined;
  const closed = win.state === "closed" ? win : closedBySelf ? { state: "closed" as const, reason: closedBySelf } : undefined;
  const requested = new Set(seen.map((d) => d.fingerprint));
  // Devices that started pairing: only while the window is open, and not
  // once their pairing was answered (added, declined, or undone).
  const requests = useLive(pairRequestsStore).value ?? [];
  const windowOpen = win.state === "open" && Boolean(mesh?.discoverable_until);
  const started = windowOpen
    ? (mesh?.pairing_started ?? []).filter(
        (p) => !rows.some((r) => r.fingerprint === p.fingerprint) && !requests.some((q) => q.fingerprint === p.fingerprint && q.state !== "pending"),
      )
    : [];
  const hidden = hiddenLine(list?.hidden_count ?? 0);
  const onTailscale = net?.id === "tailscale" || list?.networks.some((n) => n.id === "tailscale");

  // The same state the Mesh network card shows: no ✓ before it's Reachable.
  const ready = cardState(self, host, settling());
  const found = seen.length > 0;
  const help = (
    <>
      <p className={styles.help}>
        <b>Don't see it?</b> On that device, open mads → Settings → Paired devices → <b>Set up mesh</b> (or <b>Add devices</b>),
        choosing the same network. A device without a screen: <code>mads mesh setup</code>.
      </p>
      {onTailscale && list?.hidden_known ? (
        <p className={styles.help}>
          Only devices running mads with setup open are listed.{" "}
          {list.hidden_count > 0 && (
            <>
              <b>{hidden.slice(0, hidden.indexOf(" ("))}</b>
              {hidden.slice(hidden.indexOf(" ("))}
            </>
          )}
        </p>
      ) : (
        <p className={styles.help}>Only devices running mads with setup open are listed. Cameras, printers and other devices on the network never are.</p>
      )}
      <p className={styles.help}>
        Not on the same network right now?{" "}
        <button type="button" className={styles.linkButton} onClick={onUseCode}>
          Use a pairing code instead
        </button>
        .
      </p>
    </>
  );

  return (
    <>
      {net && (justSet || ready.dot !== "ok") && (
        <p className={styles.done} data-setup-done={ready.dot === "ok" ? "" : undefined} data-setup-state={ready.text}>
          {ready.dot === "ok" ? (
            <>
              <span className={styles.doneMark} aria-hidden="true">
                ✓
              </span>
              <span>
                This {host} is reachable on <b>{word}</b>. Only devices you add get in.
              </span>
            </>
          ) : ready.dot === "muted" ? (
            <>
              <span className={styles.readyMark} aria-hidden="true">
                <span className={styles.spinner} />
              </span>
              <span>
                Getting this {host} ready on <b>{word}</b>…
              </span>
            </>
          ) : (
            <>
              <span className={styles.failMark} aria-hidden="true">
                !
              </span>
              <span>{ready.problem?.title ?? ready.tip}</span>
            </>
          )}
        </p>
      )}
      {closed ? (
        <div className={styles.banner} data-tone="warn" role="status" data-window-closed={closed.reason ?? ""}>
          <span className={styles.bannerIcon} aria-hidden="true">
            !
          </span>
          <div className={styles.bannerMessage}>
            <span className={styles.bannerTitle}>{windowClosedLine(closed.reason).title}</span>
            <span className={styles.bannerDescription}>{windowClosedLine(closed.reason).description}</span>
          </div>
          <Button size="sm" onClick={reopen}>
            Open again
          </Button>
        </div>
      ) : (
        <div className={styles.foundHead} role="status">
          {!found && <span className={styles.spinner} aria-hidden="true" />}
          {found ? "Found on " : "Looking for mads devices on "}
          {list?.networks.length ? list.networks.map((n) => n.label).join(" and ") : word}
          {found ? ". Still looking for more." : "…"}
        </div>
      )}
      {win.state === "error" && (
        <p className={styles.error} role="alert">
          This {host} couldn't be made findable: {win.error}
        </p>
      )}
      {error && (
        <p className={styles.error} role="alert">
          {discoveryErrorLine(error, host, mesh)}
        </p>
      )}
      {seen.length > 0 ? (
        <ul className={styles.foundList} aria-label="mads devices found">
          {seen.map((d) => (
            <FoundDevice key={d.fingerprint} device={d} selfName={selfName} host={host} rows={rows} onAdded={onAdded} gettingReady={(mesh?.listening.length ?? 0) === 0} />
          ))}
        </ul>
      ) : (
        <div className={styles.foundList}>
          <p className={styles.empty}>No mads devices found yet.</p>
        </div>
      )}
      {started.length > 0 && (
        <ul className={styles.help} aria-label="Devices that started pairing" data-pairing-started="">
          {started.slice(0, 3).map((p) => (
            <li key={`${p.fingerprint}-${p.at}`}>
              <b>{p.name || p.short}</b> ({p.short}) started pairing with this {host} {relTime(p.at)}
              {requested.has(p.fingerprint) ? "." : ". Its request shows here when it asks; if you don't know it, ignore it."}
            </li>
          ))}
        </ul>
      )}
      {found ? (
        <Disclosure inline summary="Don't see a device?" className={styles.helpFold}>
          {help}
        </Disclosure>
      ) : (
        help
      )}
    </>
  );
}
