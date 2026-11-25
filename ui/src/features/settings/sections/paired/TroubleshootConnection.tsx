import { useEffect, useRef, useState } from "react";
import { Button } from "../../../../components/Button/Button";
import { Chip } from "../../../../components/Chip/Chip";
import { Disclosure } from "../../../../components/Disclosure/Disclosure";
import { SettingsCard } from "../../SettingsCard";
import { devicesApi, devicesStore, type DeviceRow, type SelfMesh } from "../../../../router/devices";
import { dateTime, relTime } from "./model";
import { addressWords, daysUntil, effectiveReachability, networkTitle, inDays, joinAnd, rowNetworks, rowProblem, sourceWords, TAILSCALE_ADMIN } from "./network";
import paired from "./PairedDevices.module.css";
import styles from "./Mesh.module.css";

export interface TroubleshootConnectionProps {
  row: DeviceRow;
  host: string;
  mesh: SelfMesh | undefined;
  /** Paste a pairing code… (updates its addresses). */
  onPaste: () => void;
  onToast: (t: { tone: "ok" | "info" | "bad"; title: string; description?: string }) => void;
}

/** The chip on the disclosure's summary, when there's a problem. */
function summaryChip(row: DeviceRow) {
  const reach = effectiveReachability(row);
  if (row.diagnosis === "tailscale_signed_out" && reach !== "connected") return <Chip tone="bad">Signed out of Tailscale</Chip>;
  if (reach === "unreachable") return <Chip tone="bad">Offline</Chip>;
  if (reach === "no_shared_network") return <Chip tone="bad">Not reachable</Chip>;
  if (row.diagnosis === "tailscale_key_expiring") return <Chip tone="warn">Tailscale expires soon</Chip>;
  return null;
}

/** "Connected since 08:12" today, else "Connected since Nov 22, 08:12"; plain "Connected" without a time. */
export function connectedSince(iso: string | undefined, now: Date = new Date()): string {
  const t = iso ? new Date(iso) : undefined;
  if (!t || Number.isNaN(t.getTime()) || t.getFullYear() < 2000) return "Connected";
  const hm = `${String(t.getHours()).padStart(2, "0")}:${String(t.getMinutes()).padStart(2, "0")}`;
  return t.toDateString() === now.toDateString() ? `Connected since ${hm}` : `Connected since ${dateTime(iso)}`;
}

const portOf = (addr: string | undefined): string => /:(\d+)$/.exec(addr ?? "")?.[1] ?? "17380";

/** The diagnosis: what's wrong and what to do (F4, and "Tailscale sign-in"). */
export function Diagnosis({ row: given, host, mesh }: { row: DeviceRow; host: string; mesh: SelfMesh | undefined }) {
  // One source for the state: the same the Status column reads.
  const reach = effectiveReachability(given);
  const row = { ...given, reachability: reach };
  const dn = row.name;
  const admin = TAILSCALE_ADMIN;
  const nets = rowNetworks(row);
  if (row.diagnosis === "tailscale_signed_out" && row.reachability !== "connected") {
    const onlyTs = nets.length <= 1;
    return (
      <div className={styles.reach} role="status" data-diagnosis="tailscale_signed_out">
        <span className={styles.reachIcon} aria-hidden="true">
          !
        </span>
        <p className={styles.reachTitle}>{dn} is probably signed out of Tailscale</p>
        <p className={styles.reachFacts}>
          Its Tailscale sign-in was due to expire, and it stopped answering over Tailscale.
          {onlyTs ? ` Tailscale was the only way this ${host} reaches it.` : ""}
        </p>
        <ul className={styles.reachSteps}>
          <li>
            On {dn}, sign in to Tailscale again: the Tailscale app, or <code>sudo tailscale up</code> and open the link it prints, from
            any device.
          </li>
          <li>
            So it doesn't happen again:{" "}
            <a href={admin} target="_blank" rel="noreferrer">
              open {dn} in the Tailscale admin console
            </a>{" "}
            and turn off key expiry.
          </li>
        </ul>
      </div>
    );
  }
  if (row.diagnosis === "tailscale_key_expiring" && reach === "connected") {
    const days = daysUntil(row.remote?.tailscale_key_expiry) ?? 0;
    return (
      <div className={styles.reach} data-tone="warn" role="status" data-diagnosis="tailscale_key_expiring">
        <span className={styles.reachIcon} aria-hidden="true">
          !
        </span>
        <p className={styles.reachTitle}>
          {dn}'s Tailscale sign-in expires {inDays(days)}
        </p>
        <p className={styles.reachFacts}>
          After that, Tailscale signs {dn} out and this {host} can't reach it until someone signs in on {dn}.
        </p>
        <ul className={styles.reachSteps}>
          <li>
            <a href={admin} target="_blank" rel="noreferrer">
              Open {dn} in the Tailscale admin console
            </a>{" "}
            and turn off key expiry. Recommended for a machine you reach remotely.
          </li>
          <li>Or sign in again on {dn} before it expires.</li>
        </ul>
      </div>
    );
  }
  if (row.reachability === "no_shared_network") {
    const mine = (mesh?.networks ?? []).filter((n) => n.available && n.kind !== "public").map(networkTitle);
    const usesTs = (row.addresses ?? []).some((a) => a.network === "tailscale" || a.label === "Tailscale");
    const usesVpn = usesTs || (row.addresses ?? []).some((a) => a.kind === "overlay");
    return (
      <div className={styles.reach} role="status" data-diagnosis="no_shared_network">
        <span className={styles.reachIcon} aria-hidden="true">
          !
        </span>
        <p className={styles.reachTitle}>{dn} isn't reachable from this network</p>
        <p className={styles.reachFacts}>
          {nets.length > 0 && (
            <>
              {dn} uses <b>{joinAnd(nets)}</b>.{" "}
            </>
          )}
          This {host} is on <b>{mine.length ? joinAnd(mine) : "no network it can share"}</b> right now. Devices only connect over a
          network they share.
        </p>
        <ul className={styles.reachSteps}>
          {usesVpn ? (
            <li>
              Turn on <b>{usesTs ? "Tailscale" : "the same VPN"}</b> on this {host}. It's how your devices reach each other away from
              home.
            </li>
          ) : (
            <li>
              To reach {dn} from anywhere, put the same VPN on both (such as <b>Tailscale</b>, <b>ZeroTier</b> or <b>NetBird</b>) and
              choose it under <b>Mesh network</b> on {dn}.
            </li>
          )}
          <li>Or go back to a network {dn} uses.</li>
          <li className={styles.reachLater}>Later: a mads relay will connect devices that share no network.</li>
        </ul>
      </div>
    );
  }
  if (row.reachability === "unreachable") {
    const tried = (row.addresses ?? []).filter((a) => a.state === "not_answering" || a.state === "other_device");
    const other = (row.addresses ?? []).find((a) => a.state === "other_device");
    const shared = [...new Set(tried.map((a) => a.label || a.addr))];
    return (
      <div className={styles.reach} data-tone="warn" role="status" data-diagnosis="unreachable">
        <span className={styles.reachIcon} aria-hidden="true">
          ?
        </span>
        <p className={styles.reachTitle}>{dn} isn't answering</p>
        <p className={styles.reachFacts}>
          {shared.length > 0 ? (
            <>
              You share <b>{joinAnd(shared)}</b>, but nothing answered there.
            </>
          ) : (
            "Nothing answered where this device looked for it."
          )}
          {other && <> A different device answered on {other.label || "one of its addresses"}.</>}
        </p>
        <ul className={styles.reachSteps}>
          <li>Check {dn} is awake and mads is running.</li>
          <li>Check {dn}'s firewall lets mads in (port {portOf(row.addresses?.[0]?.addr)}).</li>
          {tried.some((a) => a.kind === "lan") && (
            <li>Does this Wi-Fi stop devices from reaching each other? That's common on campus and hotel Wi-Fi.</li>
          )}
          <li>If {dn}'s address changed, paste a new pairing code from it: this {host} updates the address.</li>
        </ul>
      </div>
    );
  }
  return null;
}

/**
 * Troubleshoot connection (U3, F3, F4), in a row's details: closed unless
 * there's a problem, and open by itself when there is. The diagnosis, and
 * the only place addresses appear: where this device looks for the other,
 * each with its state, plus Try again now and Paste a pairing code….
 */
export function TroubleshootConnection({ row, host, mesh, onPaste, onToast }: TroubleshootConnectionProps) {
  const problem = rowProblem(row);
  const [trying, setTrying] = useState(false);
  const addrs = row.addresses ?? [];
  const tryAgain = async () => {
    if (!row.fingerprint) return;
    setTrying(true);
    try {
      const r = await devicesApi.redial(row.fingerprint);
      await devicesStore.refresh();
      onToast(
        r.reachability === "connected"
          ? { tone: "ok", title: "Tried again", description: `Connected${r.path?.label ? ` via ${r.path.label}` : ""}.` }
          : { tone: "info", title: "Tried again", description: `Still not reachable.${r.reason ? ` ${r.reason}` : ""}` },
      );
    } catch (err) {
      onToast({ tone: "bad", title: "Couldn't try again", description: err instanceof Error ? err.message : String(err) });
    } finally {
      setTrying(false);
    }
  };
  // Opens by itself when a problem arrives; once open (by itself or by the
  // user) it stays open when the problem clears, so what's being read
  // doesn't vanish.
  const wrap = useRef<HTMLDivElement>(null);
  // Disclosure renders open={defaultOpen}: pass only the first value, so a
  // later change never closes what's being read.
  const [openAtFirst] = useState(problem);
  useEffect(() => {
    const d = wrap.current?.querySelector("details");
    if (problem && d && !d.open) d.open = true;
  }, [problem]);
  return (
    <div ref={wrap} className={styles.troubleWrap}>
    <Disclosure
      inline
      defaultOpen={openAtFirst}
      className={styles.trouble}
      summary={
        <span className={styles.troubleSummary} data-troubleshoot={problem ? "problem" : "ok"}>
          Troubleshoot connection {summaryChip(row)}
        </span>
      }
    >
      <div className={styles.troubleBody}>
        <Diagnosis row={row} host={host} mesh={mesh} />
        <div data-paths="">
          <SettingsCard className={`${paired.detailCard} ${styles.pathCard}`}>
          <div className={styles.pathHead}>
            <h4 className={styles.pathTitle}>
              Where this {host} looks for {row.name}
            </h4>
            <span className={styles.pathHeadNote}>Tried in this order.</span>
          </div>
          {addrs.length === 0 ? (
            <p className={styles.pathEmpty}>No addresses yet. Paste a pairing code from {row.name} to add them.</p>
          ) : (
            <ul className={styles.pathList}>
              {addrs.map((a) => {
                const w = addressWords(a.state, host, a.state === "in_use" ? (row.path?.rtt_ms ?? row.rtt_ms) : undefined, a.error);
                const now = a.state === "in_use" || a.state === "works";
                return (
                  <li key={a.addr} className={styles.pathItem} data-off={w.off || undefined} data-address-state={a.state}>
                    <span className={paired.dot} data-dot={w.dot} aria-hidden="true" />
                    <span className={styles.pathWhere}>
                      <span className={styles.pathLabel}>{a.label || (a.kind === "lan" ? "Local network" : a.kind === "overlay" ? "VPN" : "Address")}</span>
                      <span className={styles.pathAddr}>{a.addr}</span>
                    </span>
                    <span className={styles.pathState}>
                      {a.state === "in_use" ? <b>{w.state}</b> : w.state}
                      <small>
                        {sourceWords(a.source, row.name)}
                        {a.added_at ? ` · ${relTime(a.added_at)}` : ""}
                      </small>
                    </span>
                    <span className={styles.pathWhen}>{now ? "Now" : a.last_ok_at ? `Last worked ${relTime(a.last_ok_at)}` : "Never reached"}</span>
                  </li>
                );
              })}
            </ul>
          )}
          <div className={styles.pathFoot}>
            <span>{row.path && effectiveReachability(row) === "connected" ? connectedSince(row.path.since) : "Not connected"}</span>
            <span className={styles.spacer} />
            <Button size="sm" disabled={trying || !row.fingerprint} onClick={() => void tryAgain()}>
              {trying ? "Trying…" : "Try again now"}
            </Button>
            <Button size="sm" onClick={onPaste}>
              Paste a pairing code…
            </Button>
          </div>
          </SettingsCard>
        </div>
      </div>
    </Disclosure>
    </div>
  );
}
