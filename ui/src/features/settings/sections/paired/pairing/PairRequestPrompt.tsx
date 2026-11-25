import { useEffect, useId, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog } from "../../../../../components/Dialog/Dialog";
import { FingerprintGroups } from "../../../../../components/Fingerprint/Fingerprint";
import { CheckCode } from "../../../../../components/CheckCode/CheckCode";
import { KindIcon } from "../../../../../components/KindIcon/KindIcon";
import { devicesApi, DevicesError, devicesStore, pairRequestsStore, type Level, type PairRequest } from "../../../../../router/devices";
import { LevelChoice } from "../LevelChoice";
import { KIND_LABEL, kindOf, platformLabel, raiseLine, shortFingerprint, SUGGESTED, VERB, type Kind } from "../model";
import { promptMode, refusalLine, withheldLine } from "../network";
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

/**
 * What the prompt shows, as a key: the fingerprint for a code, Tailscale's
 * confirmation, the number (with which number), or a found device with no
 * number at all (decline only).
 */
export function promptView(req: PairRequest): string {
  const mode = promptMode(req);
  if (mode === "number") return req.check_code ? `number:${req.check_code}:${req.vouch_withheld ?? ""}` : "number-missing";
  return mode;
}

const canApprove = (view: string) => view !== "number-missing";

/** What an approval says the prompt showed (R5): only what it actually rendered. */
export function shownFor(view: string): { check_code_confirmed?: boolean; shown_vouched?: boolean } | undefined {
  if (view === "vouched") return { shown_vouched: true };
  if (view.startsWith("number:")) return { check_code_confirmed: true };
  return undefined;
}

/**
 * The prompt's Add names the level it approves at. At control it is the
 * confirmation too: the warning of what control lets it do is on screen
 * (RaiseDialog's words), so no second dialog asks again (the v4 mockup).
 */
export const ADD_LABEL: Record<Level, string> = {
  none: "Add without access",
  view: "Add with view",
  chat: "Add with chat",
  control: "Add with control",
  admin: "Add with administer",
};

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
 * The host's approval prompt, "<device> wants to join your mesh": who's
 * asking, and what it may do here, never administer. A pairing code's
 * request shows the whole fingerprint to compare; a found device's shows
 * Tailscale's confirmation (both ends vouched) or else the check code
 * (U4), and the approval states which it showed. A key already paired
 * here says so: approving replaces that pairing (F6 R2). Dismissing
 * declines.
 */
export function PairRequestPrompt({ request, selfName, host, onDone }: PairRequestPromptProps) {
  const kind = kindOf(request.platform);
  const [level, setLevel] = useState<Level>(() => initialApprovalLevel(kind));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const name = request.name;
  const mode = promptMode(request);
  // What the prompt shows: a found device with no number to compare can
  // only be declined.
  const view = promptView(request);
  const declineId = useId();
  // Tailscale's name for it only when it says something the name doesn't
  // ("macbook.tailnet.ts.net" for macbook said the name twice, Q12).
  const nodeDiffers = Boolean(request.tailnet_node) && request.tailnet_node!.split(".")[0].toLowerCase() !== name.toLowerCase();

  // Armed for one view: Add waits PAIR_ARM_MS after the prompt opens, and
  // again whenever what it shows changes (a vouch withdrawn, a new
  // number), with Decline focused, so a click meant for the old view never
  // approves the new one.
  const [armedFor, setArmedFor] = useState<string | null>(null);
  const armed = armedFor === view;
  useEffect(() => {
    document.getElementById(declineId)?.focus();
    const t = setTimeout(() => setArmedFor(view), PAIR_ARM_MS);
    return () => clearTimeout(t);
  }, [view, declineId]);

  const act = async (approve: boolean, shownView?: string) => {
    // Approve only what's on screen now, as it was armed.
    if (approve && (shownView !== view || !canApprove(view))) return;
    setBusy(true);
    setError(undefined);
    try {
      // The approve answer names the device as it was pinned (a name
      // already taken gets -2, -3). A found device's approval says what
      // the prompt showed: Tailscale's vouch, or the number compared.
      const out = approve ? await devicesApi.approve(request.id, level, shownFor(view)) : await devicesApi.decline(request.id);
      void pairRequestsStore.refresh();
      void devicesStore.refresh();
      onDone({ approved: approve, level, name: (approve && out?.name) || name });
    } catch (err) {
      const code = err instanceof DevicesError ? err.code : undefined;
      setError(refusalLine(code, name, err instanceof Error ? err.message : String(err)));
      setBusy(false);
      // The vouch no longer holds: the list re-reads it, and the prompt shows the number.
      if (code === "vouch_withheld" || code === "check_code_required") void pairRequestsStore.refresh();
      // Answered elsewhere or expired: the list drops it, and the prompt with it.
      if (err instanceof DevicesError && (err.code === "expired" || err.code === "already_answered")) {
        void pairRequestsStore.refresh();
      }
    }
  };

  // Add approves at once, control included: the prompt offers no
  // administer, and at control its warning is on screen with a button that
  // names the level, so Add is the confirmation (Q13).
  const pair = () => {
    if (!armed || !canApprove(view)) return;
    void act(true, view);
  };

  return (
    <Dialog
      alert
      title={request.repair ? `${name} wants to pair again` : `${name} wants to join your mesh`}
      onDismiss={() => !busy && void act(false)}
      actions={
        <>
          <Button variant="ghost" disabled={busy} onClick={() => void act(false)} data-autofocus="" id={declineId}>
            Decline
          </Button>
          {canApprove(view) && (
          <span className={styles.arming} data-armed={armed || undefined} key={view}>
            <Button
              variant="primary"
              disabled={busy || !armed}
              onClick={pair}
              title={armed ? undefined : "Read it first: Add is ready in a moment"}
            >
              {ADD_LABEL[level]}
            </Button>
          </span>
          )}
        </>
      }
    >
      <div className={styles.preview}>
        <KindIcon kind={kind} label={platformLabel(request.platform)} />
        <span>
          <b>{name}</b>{" "}
          <span className={styles.meta}>
            {platformLabel(request.platform)} · {shortFingerprint(request.fingerprint)}
            {nodeDiffers ? ` · Tailscale says ${request.tailnet_node}` : ""} ·{" "}
            {mode === "code" ? `used this ${host}'s pairing code` : `on ${request.network_label || "the mesh network"}`}
          </span>
        </span>
      </div>
      {request.repair && (
        <p className={styles.help} data-repair="">
          {name} wants to pair again. It's paired here now (may {VERB[(request.repair.level || "none") as Level] ?? request.repair.level}); approving
          replaces that.
        </p>
      )}
      {request.name_collides && request.vouch_withheld !== "name_collides" && (
        <div className={styles.raise} role="note" data-name-collides="">
          {mode === "code"
            ? `You already have a device called ${name}. Compare fingerprints before you pair: this may not be the one you think.`
            : `You already have a device called ${name}: this may not be the one you think.`}
        </div>
      )}
      {mode === "code" && (
        <div className={styles.compare}>
          <FingerprintGroups fingerprint={request.fingerprint} big />
          <p className={styles.help}>
            Compare with the fingerprint next to <b>This device</b> on {name}, in Settings → Paired devices: click it to see every
            group. Every group should match.
          </p>
        </div>
      )}
      {mode === "vouched" && (
        <p className={styles.grant} data-vouched="">
          <span className={styles.vouchMark} aria-hidden="true">
            ✓
          </span>{" "}
          Tailscale confirms {name} is signed in to your tailnet as you.
        </p>
      )}
      {view === "number-missing" && (
        <p className={styles.raise} role="note" data-no-check-code="">
          {name} found this {host}, but there's no number to compare, so it can't be added here. Decline it, and use a pairing
          code.
        </p>
      )}
      {mode === "number" && request.check_code && (
        <>
          {request.vouch_withheld && <p className={styles.help}>{withheldLine(request.vouch_withheld, name)}</p>}
          <CheckCode code={request.check_code}>Is this the number on {name}? If not, decline: another device is asking.</CheckCode>
        </>
      )}
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
      <p className={styles.note}>
        {mode === "code" ? `Not expecting ${name}? Decline. The code is used up either way.` : `Not expecting ${name}? Decline. It can't ask again while this setup is open.`}
      </p>
    </Dialog>
  );
}
