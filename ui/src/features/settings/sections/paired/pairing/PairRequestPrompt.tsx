import { useEffect, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog } from "../../../../../components/Dialog/Dialog";
import { FingerprintGroups } from "../../../../../components/Fingerprint/Fingerprint";
import { KindIcon } from "../../../../../components/KindIcon/KindIcon";
import { devicesApi, DevicesError, devicesStore, pairRequestsStore, type Level, type PairRequest } from "../../../../../router/devices";
import { LevelChoice } from "../LevelChoice";
import { RaiseDialog } from "../RaiseDialog";
import { KIND_LABEL, kindOf, needsConfirm, platformLabel, raiseLine, SUGGESTED, VERB, type Kind } from "../model";
import styles from "./Pairing.module.css";

/**
 * What the prompt's level starts at (Drew to choose; switch here):
 *   "suggested": the kind's suggestion, preselected (control for a
 *                 computer, with the raise warning showing);
 *   "view":       view, with the suggestion one click away.
 */
export const APPROVAL_DEFAULT: "suggested" | "view" = "suggested";

/** The level a request's prompt starts at, under APPROVAL_DEFAULT. */
export function initialApprovalLevel(kind: Kind, policy: "suggested" | "view" = APPROVAL_DEFAULT): Level {
  return policy === "view" ? "view" : SUGGESTED[kind];
}

/** How long Pair stays disabled after the prompt opens: no approving by a stray Enter or click. */
export const PAIR_ARM_MS = 1000;

export interface PairRequestPromptProps {
  request: PairRequest;
  /** This device's own name, and its short word ("this pop-os"). */
  selfName: string;
  host: string;
  /** After Pair or Decline, with what happened, for a toast. */
  onDone: (outcome: { approved: boolean; level?: Level; name: string }) => void;
}

/**
 * The host's approval prompt: who's asking (name, platform and the whole
 * fingerprint, to compare with the other device's), and what it may do
 * here, never administer. Dismissing declines: the code is spent either way.
 */
export function PairRequestPrompt({ request, selfName, host, onDone }: PairRequestPromptProps) {
  const kind = kindOf(request.platform);
  const [level, setLevel] = useState<Level>(() => initialApprovalLevel(kind));
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => setArmed(true), PAIR_ARM_MS);
    return () => clearTimeout(t);
  }, []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const name = request.name;

  const act = async (approve: boolean) => {
    setBusy(true);
    setError(undefined);
    try {
      // The approve answer names the device as it was pinned (a name
      // already taken gets -2, -3).
      const out = approve ? await devicesApi.approve(request.id, level) : await devicesApi.decline(request.id);
      void pairRequestsStore.refresh();
      void devicesStore.refresh();
      onDone({ approved: approve, level, name: (approve && out?.name) || name });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
      // Answered elsewhere or expired: the list drops it, and the prompt with it.
      if (err instanceof DevicesError && (err.code === "expired" || err.code === "already_answered")) {
        void pairRequestsStore.refresh();
      }
    }
  };

  // Pairing at control asks first, as raising a row to control does.
  const [confirming, setConfirming] = useState(false);
  const pair = () => {
    if (needsConfirm("none", level)) setConfirming(true);
    else void act(true);
  };
  if (confirming) {
    return (
      <RaiseDialog
        device={name}
        level={level}
        selfName={selfName}
        host={host}
        onCancel={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false);
          void act(true);
        }}
      />
    );
  }

  return (
    <Dialog
      alert
      title={`${name} wants to pair`}
      onDismiss={() => !busy && void act(false)}
      actions={
        <>
          <Button variant="ghost" disabled={busy} onClick={() => void act(false)} data-autofocus="">
            Decline
          </Button>
          <span className={styles.arming} data-armed={armed || undefined}>
            <Button
              variant="primary"
              disabled={busy || !armed}
              onClick={pair}
              title={armed ? undefined : "Read it first: Pair is ready in a moment"}
            >
              Pair
            </Button>
          </span>
        </>
      }
    >
      <div className={styles.preview}>
        <KindIcon kind={kind} label={platformLabel(request.platform)} />
        <span>
          <b>{name}</b>{" "}
          <span className={styles.meta}>
            {platformLabel(request.platform)}
            {request.tailnet_node ? ` · on your tailnet as ${request.tailnet_node}` : ""} · used this {host}'s pairing code
          </span>
        </span>
      </div>
      {request.name_collides && (
        <div className={styles.raise} role="note" data-name-collides="">
          You already have a device called {name}. Compare fingerprints before you pair: this may not be the one you think.
        </div>
      )}
      <div className={styles.compare}>
        <FingerprintGroups fingerprint={request.fingerprint} big columns={7} />
        <p className={styles.help}>
          Compare with <code>mads device</code> on {name}. Every group should match.
        </p>
      </div>
      <div className={styles.ask}>
        <LevelChoice subject={name} where={`on this ${host}`} value={level} onChange={setLevel} disabled={busy} />
        <span className={styles.help}>
          Suggested for a {KIND_LABEL[kind]}
          {kind === "unknown" ? "" : ` (${platformLabel(request.platform)})`}:{" "}
          {level === SUGGESTED[kind] ? (
            VERB[SUGGESTED[kind]]
          ) : (
            <button type="button" className={styles.suggestLink} onClick={() => setLevel(SUGGESTED[kind])} disabled={busy}>
              {VERB[SUGGESTED[kind]]}
            </button>
          )}
          . You can change it later; administer is only set from the row.
        </span>
        {level === "control" && <div className={styles.raise}>{raiseLine(name, "control", selfName)}</div>}
      </div>
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      <p className={styles.note}>Not expecting {name}? Decline. The code is used up either way.</p>
    </Dialog>
  );
}
