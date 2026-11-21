import { useState } from "react";
import { Button } from "../Button/Button";
import { Popover } from "../Popover/Popover";
import styles from "./Fingerprint.module.css";

const groupsOf = (fp: string) => fp.match(/.{1,4}/g) ?? [];

/** The whole fingerprint in groups of four, the short form's four in the accent. */
export function FingerprintGroups({ fingerprint, big, columns = 4 }: { fingerprint: string; big?: boolean; columns?: 4 | 7 }) {
  return (
    <div
      className={[styles.full, big && styles.big].filter(Boolean).join(" ")}
      style={{ gridTemplateColumns: `repeat(${columns}, auto)` }}
      aria-label={`Fingerprint ${groupsOf(fingerprint).join(" ")}`}
    >
      {groupsOf(fingerprint).map((g, i) => (
        <span key={i} aria-hidden="true">
          {g}
        </span>
      ))}
    </div>
  );
}

/**
 * A device's short fingerprint as a pill; it opens to the whole one, with
 * Copy. Slashed zeros throughout, so 0 never passes for O.
 */
export function FingerprintChip({ fingerprint, label, help }: { fingerprint: string; label: string; help: string }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const groups = groupsOf(fingerprint);
  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      className={styles.popover}
      anchor={
        <button
          type="button"
          className={styles.chip}
          aria-expanded={open}
          aria-haspopup="dialog"
          title="Show the full fingerprint"
          onClick={() => {
            setOpen((o) => !o);
            setCopied(false);
          }}
        >
          <span className={styles.srOnly}>Fingerprint </span>
          {groups.slice(0, 4).join("-")}
        </button>
      }
    >
      <div role="dialog" aria-label={`${label} fingerprint`}>
        <div className={styles.label}>{label} · fingerprint</div>
        <div className={styles.groups}>
          {groups.map((g, i) => (
            <span key={i}>{g}</span>
          ))}
        </div>
        <p className={styles.help}>{help} The first four groups are its short form.</p>
        <div className={styles.row}>
          <Button
            size="sm"
            onClick={() => {
              void navigator.clipboard?.writeText(groups.join("-")).catch(() => {});
              setCopied(true);
            }}
          >
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      </div>
    </Popover>
  );
}
