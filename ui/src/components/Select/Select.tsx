import { Fragment, useEffect, useRef, useState, type KeyboardEvent } from "react";
import styles from "./Select.module.css";

export interface SelectOption {
  value: string;
  label: string;
  /** Groups adjacent options under a non-interactive header (e.g. provider name). */
  groupLabel?: string;
}

export interface SelectProps {
  value: string;
  options: SelectOption[];
  onChange: (value: string) => void;
  /** Shown on the trigger when `value` doesn't match any option (e.g. the empty "none" state). */
  placeholder?: string;
  id?: string;
  disabled?: boolean;
  className?: string;
}

/**
 * Themed stand-in for a native `<select>` over a closed set of options —
 * button trigger + listbox popup, so it can carry the app's own chevron and
 * option styling instead of the browser's. Implicit `<label>` association
 * still works because the trigger is a real `<button>`.
 */
export function Select({ value, options, onChange, placeholder = "—", id, disabled, className }: SelectProps) {
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);

  const selectedIndex = options.findIndex((o) => o.value === value);
  const selected = selectedIndex >= 0 ? options[selectedIndex] : undefined;

  useEffect(() => {
    if (!open) return;
    setHighlighted(Math.max(selectedIndex, 0));
    function handlePointerDown(e: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", handlePointerDown);
    return () => document.removeEventListener("mousedown", handlePointerDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  function commit(index: number) {
    const opt = options[index];
    if (opt) onChange(opt.value);
    setOpen(false);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>) {
    if (disabled) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      if (!open) setOpen(true);
      else setHighlighted((i) => Math.min(i + 1, options.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) setOpen(true);
      else setHighlighted((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      if (open) commit(highlighted);
      else setOpen(true);
    } else if (e.key === "Escape") {
      if (open) {
        e.preventDefault();
        setOpen(false);
      }
    }
  }

  return (
    <div className={[styles.root, className].filter(Boolean).join(" ")} ref={rootRef}>
      <button
        type="button"
        id={id}
        className={styles.trigger}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={handleKeyDown}
      >
        <span className={selected ? styles.value : styles.placeholder}>
          {selected ? selected.label : placeholder}
        </span>
        <span className={styles.chevron} aria-hidden="true" />
      </button>
      {open && (
        <ul className={styles.listbox} role="listbox">
          {options.map((opt, index) => (
            <Fragment key={opt.value}>
              {opt.groupLabel && opt.groupLabel !== options[index - 1]?.groupLabel && (
                <li className={styles.groupLabel} role="presentation">
                  {opt.groupLabel}
                </li>
              )}
              <li
                role="option"
                aria-selected={opt.value === value}
                className={[styles.option, index === highlighted && styles.optionHighlighted]
                  .filter(Boolean)
                  .join(" ")}
                onMouseEnter={() => setHighlighted(index)}
                onClick={() => commit(index)}
              >
                {opt.label}
              </li>
            </Fragment>
          ))}
        </ul>
      )}
    </div>
  );
}
