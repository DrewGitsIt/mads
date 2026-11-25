import { useState } from "react";
import { Button } from "../Button/Button";
import { Popover } from "../Popover/Popover";
import styles from "./Fingerprint.module.css";

const groupsOf = (fp: string) => fp.match(/.{1,4}/g) ?? [];

/** Groups per line of the whole fingerprint: its 13 groups read as two lines, 7 and 6. */
const PER_LINE = 7;

/**
 * The whole fingerprint, one format everywhere (the chip's popover, a
 * code's prompt, a row's details): groups of four joined by hyphens as
 * the chip writes them, seven to a line, the short form's four in the
 * accent, so the chip's text is literally its start.
 */
export function FingerprintGroups({ fingerprint, big }: { fingerprint: string; big?: boolean }) {
  const groups = groupsOf(fingerprint);
  const lines: string[][] = [];
  for (let i = 0; i < groups.length; i += PER_LINE) lines.push(groups.slice(i, i + PER_LINE));
  return (
    <div className={[styles.full, big && styles.big].filter(Boolean).join(" ")} aria-label={`Fingerprint ${groups.join(" ")}`}>
      {lines.map((line, l) => (
        <div key={l} aria-hidden="true">
          {line.map((g, i) => {
            const n = l * PER_LINE + i;
            return (
              <span key={i} data-short={n < 4 || undefined}>
                {g}
                {n < groups.length - 1 ? "-" : ""}
              </span>
            );
          })}
        </div>
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
        <FingerprintGroups fingerprint={fingerprint} />
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
