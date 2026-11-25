import { useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog } from "../../../../../components/Dialog/Dialog";
import { KindIcon } from "../../../../../components/KindIcon/KindIcon";
import {
  decodePairingCode,
  devicesApi,
  DevicesError,
  devicesStore,
  joinRetry,
  type DecodedCode,
  type DeviceRow,
  type JoinStatus,
  type SelfDevice,
} from "../../../../../router/devices";
import { hostWord, shortFingerprint } from "../model";
import { joinAnd, joinFailLine, sentence } from "../network";
import { NoSharedNetwork } from "../setup/NoSharedNetwork";
import { useRetryWait } from "./useRetryWait";
import styles from "./Pairing.module.css";
import mesh from "../Mesh.module.css";

export interface PasteCodeProps {
  self: SelfDevice | undefined;
  rows: DeviceRow[];
  onJoined: (status: JoinStatus) => void;
  onCancel: () => void;
  /** A paired host's code updated where this device looks for it. */
  onUpdated?: (name: string) => void;
  /** A code pasted on the page before this opened (P3). */
  initial?: string;
  now?: () => Date;
}

/** What's wrong with a decoded code before it's sent, or "". */
export function localRefusal(code: DecodedCode, self: SelfDevice | undefined, now: Date): string {
  if (self?.fingerprint && code.fingerprint === self.fingerprint) return "That's this device's own code.";
  if (code.expiresAt.getTime() <= now.getTime()) return "That code has expired. Ask for a new one.";
  return "";
}

/** The networks a code's addresses are on, by name ("Tailscale", "Home"), never the addresses. */
export function codeNetworks(code: DecodedCode): string[] {
  const out: string[] = [];
  for (const a of code.addresses) if (a.label && !out.includes(a.label)) out.push(a.label);
  return out;
}

type Outcome =
  | { kind: "no_shared_network"; networks: string[] }
  | { kind: "host_unreachable"; port: string }
  | { kind: "error"; message: string; retryable?: boolean; after?: number }
  | { kind: "update"; name: string }
  | { kind: "repair"; name: string };

/**
 * Paste a pairing code, joiner-first (F2): the code is decoded here to show
 * which device it's from and which networks it's reachable on; the Router
 * checks which of them this device is on and pairs over it, making it this
 * device's mesh network if it had none. No shared network: the "network in
 * common" tips, and Check again (the code isn't used up); a host that
 * couldn't be reached, "Couldn't reach" with its tips and Try again. A host already
 * paired here is checked first (F6 R6): still paired, Update addresses; no
 * longer, Re-pair. A failure the Router marks retryable offers Try again
 * with the same code (R7).
 */
export function PasteCode({ self, rows, onJoined, onCancel, onUpdated, initial = "", now = () => new Date() }: PasteCodeProps) {
  const [text, setText] = useState(initial);
  const [sending, setSending] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>();
  const host = hostWord(self);
  const decoded = text.trim() ? decodePairingCode(text) : null;
  const code = decoded?.ok ? decoded.code : undefined;
  const refusal = code ? localRefusal(code, self, now()) : "";
  const known = code ? rows.find((r) => r.fingerprint === code.fingerprint) : undefined;
  // Only a host last checked as still having this device skips the
  // Router's check; any other known host is asked first, and the Router
  // answers already_paired_update or repair_offered.
  const update =
    known?.pairing_there === "paired" ? { name: known.name } : outcome?.kind === "update" ? { name: outcome.name } : undefined;
  const repair = !update && outcome?.kind === "repair" ? { name: outcome.name } : undefined;
  const wait = useRetryWait(outcome, outcome?.kind === "error" ? (outcome.after ?? 0) : 0);
  const error = (decoded && !decoded.ok ? decoded.error : refusal) || (outcome?.kind === "error" ? outcome.message : undefined);
  const ready = Boolean(code) && !refusal && !sending && wait === 0;
  const nets = code ? codeNetworks(code) : [];
  const configured = Boolean(self?.mesh?.configured);
  // A network of the code's this device is on now, by name: the Router
  // decides for sure when it pairs.
  const here = nets.find((l) => (self?.mesh?.networks ?? []).some((n) => n.available && n.title === l));

  const ask = async () => {
    if (!ready) return;
    setSending(true);
    setOutcome(undefined);
    try {
      if (update) {
        const r = await devicesApi.updateAddresses(text);
        void devicesStore.refresh();
        onUpdated?.(r.name);
        onCancel();
        return;
      }
      onJoined(await devicesApi.join(text, Boolean(repair)));
    } catch (err) {
      if (err instanceof DevicesError && err.code === "no_shared_network") {
        const hn = Array.isArray(err.extra.host_networks) ? (err.extra.host_networks as unknown[]).filter((x): x is string => typeof x === "string") : [];
        setOutcome({ kind: "no_shared_network", networks: hn.length ? hn : nets });
      } else if (err instanceof DevicesError && err.code === "already_paired_update") {
        setOutcome({ kind: "update", name: typeof err.extra.name === "string" ? err.extra.name : (code?.host ?? "") });
      } else if (err instanceof DevicesError && err.code === "repair_offered") {
        setOutcome({ kind: "repair", name: typeof err.extra.name === "string" ? err.extra.name : (known?.name ?? code?.host ?? "") });
      } else if (err instanceof DevicesError && err.code === "host_unreachable") {
        setOutcome({ kind: "host_unreachable", port: typeof err.extra.port === "string" ? err.extra.port : "17380" });
      } else {
        const msg = err instanceof Error ? err.message : String(err);
        const c = err instanceof DevicesError ? err.code : undefined;
        const retry = joinRetry(err);
        const worded = retry.retryable || c === "rate_limited";
        setOutcome({ kind: "error", message: worded ? joinFailLine({ code: c, message: msg, ...retry }, code?.host ?? "That device", host) : sentence(msg), ...retry });
      }
    } finally {
      setSending(false);
    }
  };

  const retry = outcome?.kind === "no_shared_network";
  const again = (outcome?.kind === "error" && outcome.retryable) || outcome?.kind === "host_unreachable";
  const label = sending
    ? update
      ? "Updating…"
      : "Asking…"
    : update
      ? "Update addresses"
      : repair
        ? "Re-pair"
        : retry
          ? "Check again"
          : again
            ? wait > 0
              ? `Try again in ${wait} s`
              : "Try again"
            : "Ask to pair";
  const hn = code?.host ?? "";
  return (
    <Dialog
      title="Paste a pairing code"
      onDismiss={onCancel}
      actions={
        <>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!ready} onClick={() => void ask()}>
            {label}
          </Button>
        </>
      }
    >
      <div className={styles.field}>
        <label className={styles.label} htmlFor="paste-code">
          Pairing code from the other device
        </label>
        <textarea
          id="paste-code"
          className={styles.textarea}
          spellCheck={false}
          placeholder="mads-pair-2:…"
          value={text}
          data-autofocus=""
          onChange={(e) => {
            setText(e.target.value);
            setOutcome(undefined);
          }}
        />
        {error && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
      </div>
      {code && !refusal && (
        <div className={styles.preview} data-code-preview="">
          <KindIcon kind="unknown" label="Platform not known until it answers" />
          <span>
            <b>{hn}</b> <span className={styles.meta}>{shortFingerprint(code.fingerprint)}</span>
          </span>
        </div>
      )}
      {code && !refusal && update && (
        <p className={styles.help} data-update-addresses="">
          {update.name} is already paired here. This updates where this {host} looks for it.
        </p>
      )}
      {code && !refusal && repair && (
        <p className={styles.help} data-repair-offered="">
          {repair.name} no longer has this {host} paired. Re-pair asks {repair.name} to approve this {host} again; its row here stays
          as it is.
        </p>
      )}
      {code && !refusal && !update && outcome?.kind === "no_shared_network" && (
        <NoSharedNetwork
          lead={
            <>
              {hn} is reachable over <b>{joinAnd(outcome.networks.length ? outcome.networks : ["its own networks"])}</b>. This {host}{" "}
              {outcome.networks.length > 1 ? "is on neither" : "isn't on it"} right now.
            </>
          }
        />
      )}
      {code && !refusal && !update && outcome?.kind === "host_unreachable" && (
        <div className={mesh.tips} data-tips="host_unreachable">
          <p className={mesh.lede}>Couldn't reach {hn}, so the code wasn't used.</p>
          <ul>
            <li>Check {hn} is awake and mads is running.</li>
            <li>Check its firewall lets mads in (port {outcome.port}).</li>
            <li>Does this Wi-Fi stop devices from reaching each other? That's common on campus and hotel Wi-Fi.</li>
          </ul>
        </div>
      )}
      {code && !refusal && !update && !outcome && nets.length > 0 && (
        <p className={styles.grant} data-code-networks="">
          {here ? (
            <>
              This {host} can reach {hn} over <b>{here}</b>.{configured ? "" : ` Pairing makes that this ${host}'s mesh network.`}
            </>
          ) : (
            <>
              {hn} is reachable over <b>{joinAnd(nets)}</b>.
              {configured ? "" : ` If this ${host} can reach it, pairing makes that this ${host}'s mesh network.`}
            </>
          )}
        </p>
      )}
    </Dialog>
  );
}
