import { useEffect, useState, useSyncExternalStore } from "react";
import { Toast } from "../../../../../components/Toast/Toast";
import { pairRequestsStore, selfStore, useLive, type Level, type PairRequest } from "../../../../../router/devices";
import { hostWord, VERB } from "../model";
import { PairRequestPrompt } from "./PairRequestPrompt";

/**
 * Whether the approval prompt opens by itself when a request arrives: while
 * Settings → Paired devices is on screen (or its code dialog is open). Else
 * a toast says who's asking, and opens it.
 */
let autoOpeners = 0;
const autoListeners = new Set<() => void>();
export function holdPairPromptOpen(): () => void {
  autoOpeners++;
  for (const l of autoListeners) l();
  return () => {
    autoOpeners--;
    for (const l of autoListeners) l();
  };
}
const subscribeAuto = (l: () => void) => {
  autoListeners.add(l);
  return () => autoListeners.delete(l);
};
const getAuto = () => autoOpeners > 0;

/** A prompt's answer, as the host gave it. */
export type PairOutcome = { approved: boolean; level?: Level; name: string; at: number };

/**
 * The last prompt answered here, for whatever is on screen to say so in
 * place: Add devices ("macbook added"), and Paired devices under its bar.
 * While Paired devices is on screen there's no toast: it covered the
 * section's own buttons (Q6).
 */
let lastOutcome: PairOutcome | null = null;
const outcomeListeners = new Set<() => void>();
function setLastOutcome(o: PairOutcome | null) {
  lastOutcome = o;
  for (const l of outcomeListeners) l();
}
export const clearPairOutcome = () => setLastOutcome(null);
export function usePairOutcome(): PairOutcome | null {
  return useSyncExternalStore(
    (l) => {
      outcomeListeners.add(l);
      return () => outcomeListeners.delete(l);
    },
    () => lastOutcome,
  );
}

export function pendingRequests(list: PairRequest[] | undefined, now = Date.now()): PairRequest[] {
  return (list ?? []).filter((r) => r.state === "pending" && Date.parse(r.expires_at) > now).sort((a, b) => a.created_at.localeCompare(b.created_at));
}

/**
 * Pairing requests, app-wide: a device that pasted this one's code, or
 * found it during setup, waits on its user, wherever they are in the app. Mounted once by App, on the
 * window's LOCAL event bus (device.pair_requested), no stream of its own.
 */
export function PairRequestWatcher() {
  const { value } = useLive(pairRequestsStore);
  const self = useLive(selfStore).value;
  const auto = useSyncExternalStore(subscribeAuto, getAuto);
  const [openId, setOpenId] = useState<string | null>(null);
  const [toasted, setToasted] = useState<Set<string>>(() => new Set());
  const [outcome, setOutcome] = useState<{ approved: boolean; level?: Level; name: string } | null>(null);
  const pending = pendingRequests(value);
  // A pairing the joiner undid ("No, it was different"): say so once.
  const undone = (value ?? []).find(
    (r) => r.joiner_mismatch && r.decided_at && Date.now() - Date.parse(r.decided_at) < 120_000 && !toasted.has(`undone:${r.id}`),
  );
  const first = pending[0];
  const open = pending.find((r) => r.id === openId);

  useEffect(() => {
    if (auto && first && !openId) setOpenId(first.id);
  }, [auto, first, openId]);

  // A request that stops pending (answered elsewhere, expired) takes its prompt with it.
  useEffect(() => {
    if (openId && !open) setOpenId(null);
  }, [openId, open]);

  const host = hostWord(self);
  return (
    <>
      {open && (
        <PairRequestPrompt
          key={open.id}
          request={open}
          selfName={self?.name ?? "this device"}
          host={host}
          onDone={(o) => {
            setOpenId(null);
            setLastOutcome({ ...o, at: Date.now() });
            if (!auto) setOutcome(o);
          }}
        />
      )}
      {!open &&
        first &&
        !toasted.has(first.id) && (
          <Toast
            tone="info"
            title={first.repair ? `${first.name} wants to pair again` : `${first.name} wants to join your mesh`}
            description={
              first.mode === "discovered"
                ? `It found this ${host} on ${first.network_label || "your mesh network"}. Approve it, and choose what it may do here.`
                : `It used this ${host}'s pairing code. Approve it, and choose what it may do here.`
            }
            duration={Math.max(1000, Date.parse(first.expires_at) - Date.now())}
            action={{ label: "Review…", onClick: () => setOpenId(first.id) }}
            onDismiss={() => setToasted((s) => new Set(s).add(first.id))}
          />
        )}
      {outcome && (
        <Toast
          tone={outcome.approved ? "ok" : "info"}
          title={outcome.approved ? `${outcome.name} added` : "Declined"}
          description={
            outcome.approved
              ? `It may ${VERB[outcome.level ?? "none"]} mads on this ${host}.`
              : `${outcome.name} wasn't added.`
          }
          onDismiss={() => setOutcome(null)}
        />
      )}
      {undone && (
        <Toast
          tone="warn"
          title={`${undone.name} was removed`}
          description={`It said the number on its screen didn't match this ${host}'s, so the pairing was undone here.`}
          onDismiss={() => setToasted((s) => new Set(s).add(`undone:${undone.id}`))}
        />
      )}
    </>
  );
}
