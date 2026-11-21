import { Fragment, type KeyboardEvent, type MouseEvent } from "react";
import { Caret } from "../../../../components/Caret/Caret";
import { KindIcon } from "../../../../components/KindIcon/KindIcon";
import { SettingTag } from "../../SettingRow";
import { Switch } from "../../../../components/Switch/Switch";
import { Tooltip } from "../../../../components/Tooltip/Tooltip";
import { VerbPicker, type VerbOption } from "../../../../components/VerbPicker/VerbPicker";
import { SettingsCard } from "../../SettingsCard";
import { LEVELS, type DeviceRow, type Level, type PeerCeilings } from "../../../../router/devices";
import { DeviceDetails } from "./DeviceDetails";
import { isViewed, kindOfRow, LEVEL_DESC, letsCell, levelOf, platformLabel, showSessionsWhy, statusCell, VERB } from "./model";
import styles from "./PairedDevices.module.css";

/** The row's verb options: every level, administer marked "Set here only". */
export function verbOptions(): VerbOption<Level>[] {
  return LEVELS.map((l) => ({
    value: l,
    label: VERB[l],
    description: LEVEL_DESC[l],
    tag: l === "admin" ? <SettingTag tone="muted">Set here only</SettingTag> : undefined,
  }));
}

export interface DeviceTableProps {
  rows: DeviceRow[];
  /** "pop-os": this device's short name, for "this pop-os". */
  host: string;
  openId: string | null;
  onToggle: (id: string | null) => void;
  onLevel: (row: DeviceRow, level: Level) => void;
  onCeilings: (row: DeviceRow, ceilings: PeerCeilings) => void;
  onShowSessions: (row: DeviceRow, show: boolean) => void;
  onRemove: (row: DeviceRow) => void;
  /** Rows with a write in flight. */
  busy: ReadonlySet<string>;
}

// Clicks on a row's own controls don't toggle it.
const INTERACTIVE = "button, [role=switch], [role=listbox], [role=option], a, input, [data-tip-anchor]";

/**
 * One table, both directions per row (docs/mockups/paired-devices-v3.html
 * layout C): what the device may do here (editable), what it lets this
 * device do there (read-only), whether its sessions show here, and a status
 * worded by direction. A row opens its details inline beneath it, one at a
 * time; Esc or a second click closes them.
 */
export function DeviceTable({ rows, host, openId, onToggle, onLevel, onCeilings, onShowSessions, onRemove, busy }: DeviceTableProps) {
  const now = new Date();
  return (
    <SettingsCard className={styles.wrap}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Device</th>
            <th>May … mads on this {host}</th>
            <th className={styles.letsCol}>Lets this {host} …</th>
            <th>Show sessions</th>
            <th className={styles.statusHead}>Status</th>
            <th>
              <span className={styles.srOnly}>Details</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const open = openId === row.id;
            const toggle = () => onToggle(open ? null : row.id);
            const lets = letsCell(row, host, now);
            const status = statusCell(row, host, now);
            const why = showSessionsWhy(row, host);
            const viewed = isViewed(row);
            const kind = kindOfRow(row);
            const rowBusy = busy.has(row.id);
            const sw = (
              <Switch
                checked={viewed}
                onChange={(v) => onShowSessions(row, v)}
                aria-label={`Show mads sessions from ${row.name}`}
                disabled={rowBusy || (!!why && !viewed)}
              />
            );
            return (
              <Fragment key={row.id}>
                <tr
                  className={styles.row}
                  tabIndex={0}
                  aria-expanded={open}
                  aria-controls={open ? `detail-${row.id}` : undefined}
                  data-device={row.name}
                  onClick={(e: MouseEvent) => {
                    if ((e.target as HTMLElement).closest(INTERACTIVE)) return;
                    toggle();
                  }}
                  onKeyDown={(e: KeyboardEvent<HTMLTableRowElement>) => {
                    if (e.target !== e.currentTarget) return;
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      toggle();
                    } else if (e.key === "Escape" && open) {
                      e.preventDefault();
                      onToggle(null);
                    }
                  }}
                >
                  <td>
                    <span className={styles.name}>
                      <KindIcon kind={kind} label={platformLabel(row.remote?.platform)} />
                      <span className={styles.nameText}>{row.name}</span>
                    </span>
                  </td>
                  <td>
                    {row.fingerprint ? (
                      <VerbPicker<Level>
                        value={levelOf(row)}
                        options={verbOptions()}
                        onChange={(l) => onLevel(row, l)}
                        aria-label={`What ${row.name} may do with mads on this ${host}`}
                        disabled={rowBusy}
                      />
                    ) : (
                      <span className={styles.notPaired}>Not paired</span>
                    )}
                  </td>
                  <td className={styles.letsCol}>
                    <span data-tip-anchor="">
                      <Tooltip tip={lets.tip}>
                        <span className={styles.lets} data-level={lets.level} data-stale={lets.stale || undefined} data-lets="">
                          {lets.text}
                        </span>
                      </Tooltip>
                    </span>
                  </td>
                  <td>
                    {why && !viewed ? (
                      <span data-tip-anchor="">
                        <Tooltip tip={why} className={styles.switchCell}>
                          {sw}
                        </Tooltip>
                      </span>
                    ) : (
                      sw
                    )}
                  </td>
                  <td>
                    <span data-tip-anchor="">
                      <Tooltip tip={status.tip} align="end">
                        <span className={styles.status} data-status="">
                          <span className={styles.dot} data-dot={status.dot} aria-hidden="true" />
                          <span className={styles.statusText}>{status.text}</span>
                        </span>
                      </Tooltip>
                    </span>
                  </td>
                  <td className={styles.chev}>
                    <Caret className={styles.chevCaret} />
                  </td>
                </tr>
                {open && (
                  <tr className={styles.detailRow}>
                    <td colSpan={6}>
                      <DeviceDetails
                        row={row}
                        rows={rows}
                        onCeilings={(c) => onCeilings(row, c)}
                        onRemove={() => onRemove(row)}
                        busy={rowBusy}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </SettingsCard>
  );
}
