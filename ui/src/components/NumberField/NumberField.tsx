import { useEffect, useState, type InputHTMLAttributes, type KeyboardEvent } from "react";
import styles from "./NumberField.module.css";

export interface NumberFieldProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    "value" | "onChange" | "type" | "min" | "max" | "step" | "prefix"
  > {
  value: number;
  onChange: (value: number) => void;
  min?: number;
  max?: number;
  /** Stepper/arrow-key increment (default 1). */
  step?: number;
  /** Trailing unit, e.g. "ms", "days". */
  unit?: string;
  /** Leading symbol, e.g. "$". */
  prefix?: string;
  /**
   * When true (default), each keystroke that parses to a number reaches
   * onChange as typed; clamping waits for blur/Enter. When false, onChange
   * only fires on commit (blur, Enter, a step) — for fields that save on
   * every change, where saving "3" on the way to "350" would be wrong.
   */
  live?: boolean;
}

// A number in progress: digits with at most one point and a leading minus.
// Anything else is rejected at the keystroke rather than coerced later.
const PARTIAL_NUMBER = /^-?\d*\.?\d*$/;

function parse(text: string): number | null {
  if (text.trim() === "" || text === "-" || text === "." || text === "-.") return null;
  const n = Number(text);
  return Number.isFinite(n) ? n : null;
}

/**
 * Themed numeric input: our own ▲/▼ steppers and an optional prefix/unit,
 * instead of the browser's spinners (which ignore the theme). The text is
 * buffered, so an empty or half-typed edit ("", "-", "1.") stays as typed
 * instead of collapsing to 0; blur or Enter commits it, clamped to min/max,
 * and an unparseable buffer reverts to the current value. The input keeps
 * any id/aria-label it's given, so an external <label htmlFor> still names it.
 */
export function NumberField({
  value,
  onChange,
  min,
  max,
  step = 1,
  unit,
  prefix,
  live = true,
  disabled,
  className,
  onBlur,
  onKeyDown,
  ...inputProps
}: NumberFieldProps) {
  const [text, setText] = useState(String(value));

  // Resync when the value moves underneath us (a save, a profile switch) —
  // but not when it's our own live edit echoing back ("1." already reads 1).
  useEffect(() => {
    setText((t) => (parse(t) === value ? t : String(value)));
  }, [value]);

  const clamp = (n: number) => Math.min(max ?? Infinity, Math.max(min ?? -Infinity, n));

  function commit(next: number) {
    const clamped = clamp(next);
    setText(String(clamped));
    if (clamped !== value) onChange(clamped);
  }

  function commitText() {
    const n = parse(text);
    if (n === null) setText(String(value));
    else commit(n);
  }

  function stepBy(direction: 1 | -1) {
    // toPrecision trims float drift (0.1 + 0.2) without capping real decimals.
    const base = parse(text) ?? value;
    commit(Number((base + direction * step).toPrecision(12)));
  }

  function handleKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    onKeyDown?.(e);
    if (e.defaultPrevented) return;
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      stepBy(e.key === "ArrowUp" ? 1 : -1);
    } else if (e.key === "Enter") {
      commitText();
    }
  }

  const current = parse(text) ?? value;

  return (
    <div
      className={[styles.field, disabled && styles.disabled, className].filter(Boolean).join(" ")}
    >
      {prefix && <span className={styles.prefix}>{prefix}</span>}
      <input
        type="text"
        inputMode={Number.isInteger(step) && (min ?? 0) >= 0 ? "numeric" : "decimal"}
        role="spinbutton"
        aria-valuenow={value}
        aria-valuemin={min}
        aria-valuemax={max}
        autoComplete="off"
        spellCheck={false}
        {...inputProps}
        className={styles.input}
        disabled={disabled}
        value={text}
        onChange={(e) => {
          const next = e.target.value;
          if (!PARTIAL_NUMBER.test(next)) return;
          setText(next);
          const n = parse(next);
          if (live && n !== null && n !== value) onChange(n);
        }}
        onBlur={(e) => {
          commitText();
          onBlur?.(e);
        }}
        onKeyDown={handleKeyDown}
      />
      {unit && <span className={styles.unit}>{unit}</span>}
      <div className={styles.steps}>
        {/* Out of the tab order: ArrowUp/Down on the input does the same.
            mousedown is cancelled so a click doesn't pull focus off it. */}
        <button
          type="button"
          tabIndex={-1}
          aria-label="Increase"
          className={styles.step}
          disabled={disabled || (max !== undefined && current >= max)}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => stepBy(1)}
        >
          ▲
        </button>
        <button
          type="button"
          tabIndex={-1}
          aria-label="Decrease"
          className={styles.step}
          disabled={disabled || (min !== undefined && current <= min)}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => stepBy(-1)}
        >
          ▼
        </button>
      </div>
    </div>
  );
}
