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

export function pendingRequests(list: PairRequest[] | undefined, now = Date.now()): PairRequest[] {
  return (list ?? []).filter((r) => r.state === "pending" && Date.parse(r.expires_at) > now).sort((a, b) => a.created_at.localeCompare(b.created_at));
}

/**
 * Pairing requests, app-wide: a device that pasted this one's code waits on
 * its user, wherever they are in the app. Mounted once by App, on the
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
            setOutcome(o);
          }}
        />
      )}
      {!open &&
        first &&
        !toasted.has(first.id) && (
          <Toast
            tone="info"
            title={`${first.name} wants to pair`}
            description={`It used this ${host}'s pairing code. Approve it, and choose what it may do here.`}
            duration={Math.max(1000, Date.parse(first.expires_at) - Date.now())}
            action={{ label: "Review…", onClick: () => setOpenId(first.id) }}
            onDismiss={() => setToasted((s) => new Set(s).add(first.id))}
          />
        )}
      {outcome && (
        <Toast
          tone={outcome.approved ? "ok" : "info"}
          title={outcome.approved ? `${outcome.name} is paired` : "Declined"}
          description={
            outcome.approved
              ? `It may ${VERB[outcome.level ?? "none"]} mads on this ${host}.`
              : `${outcome.name} wasn't paired.`
          }
          onDismiss={() => setOutcome(null)}
        />
      )}
    </>
  );
}
