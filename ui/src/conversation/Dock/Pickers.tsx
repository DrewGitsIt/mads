import { useState } from "react";
import type { CSSProperties } from "react";
import { Popover } from "../../components/Popover/Popover";
import { modelDisplayName } from "../../router/modelDisplay";
import type { Model, ProviderBehavior, ProviderSupport, SessionTrust } from "../../router/types";
import { trustLabel, trustLevels } from "../../features/sessions/launchpad/pills/trustLevels";
import { shortModel, shortPersona, trustTone } from "./dockLabels";
import styles from "./StatusLine.module.css";

/** Popover's default placement is below its anchor; the dock's pickers open upward. */
export const UPWARD_LEFT: CSSProperties = { top: "auto", bottom: "calc(100% + 6px)", left: 0, padding: 4 };
export const UPWARD_RIGHT: CSSProperties = {
  top: "auto",
  bottom: "calc(100% + 6px)",
  left: "auto",
  right: 0,
  padding: 4,
};

export function cx(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

/**
 * Full text above ~700px, short below (container query on the dock), and
 * neither below ~520px when `iconOnly` — the icon carries the segment then.
 * One span when the short form is the same text.
 */
export function Ladder({ full, short, iconOnly }: { full: string; short: string; iconOnly?: boolean }) {
  if (full === short) {
    return <span className={cx(styles.text, iconOnly && styles.vNotMin)}>{full}</span>;
  }
  return (
    <>
      <span className={cx(styles.text, styles.vFull)}>{full}</span>
      <span className={cx(styles.text, styles.vMid, !iconOnly && styles.vMidKeep)}>{short}</span>
    </>
  );
}

interface SegmentPickerProps {
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
}

/**
 * Persona: the persona's name (never an `agt_…` id), edited in a small
 * upward popover. `value` is the raw wire value — it seeds the editor and is
 * what gets committed — and `label` is display only, so committing without
 * changes can't write a display name back into the record.
 */
export function PersonaSegment({
  value: rawValue,
  label,
  open,
  onOpen,
  onClose,
  onCommit,
}: SegmentPickerProps & { value: string; label: string; onCommit: (value: string) => void }) {
  const [value, setValue] = useState(rawValue);
  return (
    <Popover
      open={open}
      onClose={onClose}
      style={UPWARD_LEFT}
      anchor={
        <button
          type="button"
          className={styles.seg}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-label={label}
          title={`Persona: ${label}`}
          data-segment="persona"
          onClick={() => {
            if (open) return onClose();
            setValue(rawValue);
            onOpen();
          }}
        >
          <span className={styles.avatar} aria-hidden="true">
            {(label.trim()[0] ?? "?").toUpperCase()}
          </span>
          <Ladder full={label} short={shortPersona(label)} iconOnly />
          <span className={styles.caret} aria-hidden="true">
            ▾
          </span>
        </button>
      }
    >
      <div className={styles.menu} role="dialog" aria-label="Persona">
        <div className={styles.menuHead}>Persona</div>
        <form
          className={styles.personaForm}
          onSubmit={(e) => {
            e.preventDefault();
            onCommit(value);
          }}
        >
          <input
            className={styles.personaInput}
            value={value}
            autoFocus
            aria-label="Persona name"
            onChange={(e) => setValue(e.target.value)}
          />
          <button type="submit" className={cx(styles.btn, styles.btnPrimary)}>
            Save
          </button>
        </form>
      </div>
    </Popover>
  );
}

/**
 * Model: a dropdown of the session provider's own models — not a free-text
 * field. With no discovered models (provider Models() empty/failed) it is a
 * static segment rather than an empty menu.
 */
export function ModelSegment({
  provider,
  model,
  models,
  open,
  onOpen,
  onClose,
  onSelect,
}: SegmentPickerProps & { provider?: string; model: string; models: Model[]; onSelect: (id: string) => void }) {
  const label = modelDisplayName(model, models) ?? model;
  const title = `Model: ${model}${provider ? ` (${provider})` : ""}`;
  const face = (
    <>
      <span className={styles.dot} style={{ background: "var(--info)" }} aria-hidden="true" />
      <Ladder full={label} short={shortModel(label)} iconOnly />
    </>
  );

  if (models.length === 0) {
    return (
      <span className={cx(styles.seg, styles.segStatic)} title={title} aria-label={label} data-segment="model">
        {face}
      </span>
    );
  }

  return (
    <Popover
      open={open}
      onClose={onClose}
      style={UPWARD_LEFT}
      anchor={
        <button
          type="button"
          className={styles.seg}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label={label}
          title={title}
          data-segment="model"
          onClick={() => (open ? onClose() : onOpen())}
        >
          {face}
          <span className={styles.caret} aria-hidden="true">
            ▾
          </span>
        </button>
      }
    >
      <div className={styles.menu}>
        {provider && <div className={styles.menuHead}>{provider}</div>}
        <div role="listbox" aria-label="Model" className={styles.options}>
          {models.map((m) => (
            <button
              key={m.id}
              type="button"
              role="option"
              aria-selected={m.id === model}
              className={cx(styles.item, m.id === model && styles.itemSelected)}
              onClick={() => onSelect(m.id)}
            >
              {m.name}
            </button>
          ))}
        </div>
      </div>
    </Popover>
  );
}

/**
 * Trust: the session's live level, switched from an upward dropdown — the
 * affordance the blocked-permission banner's "raise trust" points at. An
 * unset trust renders as Limited, the backend's builtin default
 * (services/router/internal/api/sessiondefaults.go). Selecting a level
 * restarts the agent process (trust is spawn-time only in the runner).
 *
 * A level the provider can't run is never shown as if it were running: the
 * Router reports the fallback on the record (effective_trust), and the
 * segment reads "Trust: Auto-review → Limited" with the reason on hover. The
 * menu marks the same option "(runs as Limited on <provider>)" before it's
 * picked, from the provider's own matrix.
 *
 * The value never collapses in a narrow dock; only the "Trust: " prefix
 * drops below ~700px. Without `onSelect` it is a static segment.
 */
export function TrustSegment({
  trust,
  effectiveTrust,
  provider,
  matrix,
  open,
  onOpen,
  onClose,
  onSelect,
}: SegmentPickerProps & {
  trust?: SessionTrust;
  effectiveTrust?: SessionTrust;
  provider?: string;
  matrix?: Record<ProviderBehavior, ProviderSupport>;
  onSelect?: (trust: SessionTrust) => void;
}) {
  const levels = trustLevels(matrix, provider);
  const current = trust ?? "limited";
  const fallback = effectiveTrust && effectiveTrust !== current ? effectiveTrust : undefined;
  const asked = trustLabel(current);
  const value = fallback ? `${asked} → ${trustLabel(fallback)}` : asked;
  const label = `Trust: ${value}`;
  const reason = fallback
    ? `${provider ?? "This provider"} doesn't support ${asked.toLowerCase()}; running as ${trustLabel(fallback)}`
    : undefined;
  const face = (
    <>
      <span
        className={styles.dot}
        style={{ background: `var(--${trustTone(fallback ?? current)})` }}
        aria-hidden="true"
      />
      <span className={cx(styles.text, styles.vFull)}>Trust: </span>
      <span className={styles.text}>{value}</span>
    </>
  );

  if (!onSelect) {
    return (
      <span
        className={cx(styles.seg, styles.segStatic, styles.trust)}
        title={reason}
        aria-label={label}
        data-segment="trust"
      >
        {face}
      </span>
    );
  }

  return (
    <Popover
      open={open}
      onClose={onClose}
      style={UPWARD_LEFT}
      anchor={
        <button
          type="button"
          className={cx(styles.seg, styles.trust)}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label={label}
          title={reason ?? "Trust level — changing it restarts the agent"}
          data-segment="trust"
          onClick={() => (open ? onClose() : onOpen())}
        >
          {face}
          <span className={styles.caret} aria-hidden="true">
            ▾
          </span>
        </button>
      }
    >
      <div className={styles.menu}>
        <div className={styles.menuHead}>Trust</div>
        <div role="listbox" aria-label="Trust level" className={styles.options}>
          {levels.map((l) => (
            <button
              key={l.value}
              type="button"
              role="option"
              aria-selected={l.value === current}
              title={l.description}
              className={cx(styles.item, l.value === current && styles.itemSelected)}
              onClick={() => onSelect(l.value)}
            >
              <span
                className={styles.dot}
                style={{ background: `var(--${trustTone(l.value)})` }}
                aria-hidden="true"
              />
              {l.label}
            </button>
          ))}
        </div>
        <div className={styles.menuNote}>Changing trust restarts the agent process. The conversation continues.</div>
      </div>
    </Popover>
  );
}
