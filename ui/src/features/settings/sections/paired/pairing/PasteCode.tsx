import { useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog } from "../../../../../components/Dialog/Dialog";
import { KindIcon } from "../../../../../components/KindIcon/KindIcon";
import { decodePairingCode, devicesApi, type DecodedCode, type DeviceRow, type JoinStatus, type SelfDevice } from "../../../../../router/devices";
import { shortFingerprint } from "../model";
import styles from "./Pairing.module.css";

export interface PasteCodeProps {
  self: SelfDevice | undefined;
  rows: DeviceRow[];
  onJoined: (status: JoinStatus) => void;
  onCancel: () => void;
  now?: () => Date;
}

/** What's wrong with a decoded code before it's sent, or "". */
export function localRefusal(code: DecodedCode, self: SelfDevice | undefined, rows: DeviceRow[], now: Date): string {
  if (self?.fingerprint && code.fingerprint === self.fingerprint) return "That's this device's own code.";
  const known = rows.find((r) => r.fingerprint === code.fingerprint);
  if (known) return `${known.name} is already paired here. Remove it first to pair again.`;
  if (code.expiresAt.getTime() <= now.getTime()) return "That code has expired. Ask for a new one.";
  return "";
}

/**
 * Paste a pairing code: decoded here to show which device it's from (the
 * host checks the rest), then sent; the host approves and chooses.
 */
export function PasteCode({ self, rows, onJoined, onCancel, now = () => new Date() }: PasteCodeProps) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string>();
  const decoded = text.trim() ? decodePairingCode(text) : null;
  const refusal = decoded?.ok ? localRefusal(decoded.code, self, rows, now()) : "";
  const error = sendError ?? (decoded && !decoded.ok ? decoded.error : refusal || undefined);
  const ready = decoded?.ok && !refusal && !sending;

  const ask = async () => {
    if (!ready) return;
    setSending(true);
    setSendError(undefined);
    try {
      onJoined(await devicesApi.join(text));
    } catch (err) {
      setSendError(err instanceof Error ? err.message : String(err));
      setSending(false);
    }
  };

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
            {sending ? "Asking…" : "Ask to pair"}
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
          placeholder="mads-pair-1:…"
          value={text}
          data-autofocus=""
          onChange={(e) => {
            setText(e.target.value);
            setSendError(undefined);
          }}
        />
        {error && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
      </div>
      {decoded?.ok && !refusal && (
        <div className={styles.preview} data-code-preview="">
          <KindIcon kind="unknown" label="Platform not known until it answers" />
          <span>
            <b>{decoded.code.host}</b>{" "}
            <span className={styles.meta}>
              {shortFingerprint(decoded.code.fingerprint)} · {decoded.code.meshUrl.replace(/^http:\/\//, "")}
            </span>
          </span>
        </div>
      )}
    </Dialog>
  );
}
