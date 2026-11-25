import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Caret } from "../Caret/Caret";
import styles from "./VerbPicker.module.css";

export interface VerbOption<T extends string = string> {
  value: T;
  label: string;
  /** One line under the label: what it means, or why it can't be chosen. */
  description: string;
  disabled?: boolean;
  /** A small tag after the label ("Set here only"). */
  tag?: ReactNode;
}

export interface VerbPickerProps<T extends string = string> {
  value: T;
  options: VerbOption<T>[];
  onChange: (value: T) => void;
  "aria-label": string;
  disabled?: boolean;
  id?: string;
}

/**
 * The verb in a sentence ("may [control] mads on this pop-os"), edited in
 * place: a compact trigger and a listbox whose options explain themselves.
 * Arrow keys skip what can't be chosen.
 */
export function VerbPicker<T extends string = string>({ value, options, onChange, disabled, id, ...aria }: VerbPickerProps<T>) {
  const [open, setOpen] = useState(false);
  const [hi, setHi] = useState(0);
  const rootRef = useRef<HTMLSpanElement>(null);
  const selectedIndex = options.findIndex((o) => o.value === value);
  const selected = options[selectedIndex];

  useEffect(() => {
    if (!open) return;
    setHi(Math.max(selectedIndex, 0));
    function onDown(e: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  function step(from: number, d: 1 | -1): number {
    const n = options.length;
    for (let k = 0, i = from; k < n; k++, i = (i + d + n) % n) if (!options[i].disabled) return i;
    return from;
  }

  function pick(i: number) {
    const o = options[i];
    if (!o || o.disabled) return;
    setOpen(false);
    if (o.value !== value) onChange(o.value);
  }

  function onKeyDown(e: KeyboardEvent<HTMLButtonElement>) {
    if (disabled) return;
    const n = options.length;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) setOpen(true);
      else {
        const d = e.key === "ArrowDown" ? 1 : -1;
        setHi((h) => step((h + d + n) % n, d));
      }
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      if (open) pick(hi);
      else setOpen(true);
    } else if (e.key === "Escape" && open) {
      e.preventDefault();
      setOpen(false);
    } else if (e.key === "Tab" && open) {
      setOpen(false);
    }
  }

  return (
    <span ref={rootRef} className={styles.root} data-level={value}>
      <button
        type="button"
        id={id}
        className={styles.trigger}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        onKeyDown={onKeyDown}
        {...aria}
      >
        <span className={styles.value}>{selected?.label ?? "—"}</span>
        <Caret className={styles.chevron} />
      </button>
      {open && (
        <ul className={styles.listbox} role="listbox" aria-label={aria["aria-label"]} onClick={(e) => e.stopPropagation()}>
          {options.map((o, i) => (
            <li
              key={o.value}
              role="option"
              aria-selected={o.value === value}
              aria-disabled={o.disabled || undefined}
              className={[styles.option, i === hi && styles.highlighted].filter(Boolean).join(" ")}
              onMouseEnter={() => setHi(i)}
              onClick={() => pick(i)}
            >
              <span className={styles.optionLabel}>
                {o.label}
                {o.tag}
              </span>
              <span className={styles.optionDescription}>{o.description}</span>
            </li>
          ))}
        </ul>
      )}
    </span>
  );
}
