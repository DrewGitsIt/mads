import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import styles from "./Combobox.module.css";

export interface ComboboxProps {
  value: string;
  onChange: (value: string) => void;
  /** Known options to suggest — the value is still free text, unknown entries stay legal. */
  options: string[];
  placeholder?: string;
  id?: string;
  className?: string;
}

/**
 * Free-text input with a themed suggestion list — like a `<input list>`
 * datalist, but styled and keyboard-navigable. The value is never
 * constrained to `options`; picking one just fills the input.
 */
export function Combobox({ value, onChange, options, placeholder, id, className }: ComboboxProps) {
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const filtered = value.trim()
    ? options.filter((o) => o.toLowerCase().includes(value.trim().toLowerCase()))
    : options;

  useEffect(() => {
    setHighlighted((i) => Math.min(i, Math.max(filtered.length - 1, 0)));
  }, [filtered.length]);

  useEffect(() => {
    if (!open) return;
    function handlePointerDown(e: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", handlePointerDown);
    return () => document.removeEventListener("mousedown", handlePointerDown);
  }, [open]);

  function commit(option: string) {
    onChange(option);
    setOpen(false);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setOpen(true);
      setHighlighted((i) => Math.min(i + 1, filtered.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlighted((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      if (open && filtered[highlighted] !== undefined) {
        e.preventDefault();
        commit(filtered[highlighted]);
      }
    } else if (e.key === "Escape") {
      if (open) {
        e.preventDefault();
        setOpen(false);
      }
    }
  }

  return (
    <div className={[styles.root, className].filter(Boolean).join(" ")} ref={rootRef}>
      <input
        ref={inputRef}
        id={id}
        className={styles.input}
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        autoComplete="off"
        value={value}
        placeholder={placeholder}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
        }}
        onKeyDown={handleKeyDown}
      />
      <span className={styles.chevron} aria-hidden="true" />
      {open && filtered.length > 0 && (
        <ul className={styles.listbox} role="listbox">
          {filtered.map((opt, index) => (
            <li
              key={opt}
              role="option"
              aria-selected={opt === value}
              className={[styles.option, index === highlighted && styles.optionHighlighted]
                .filter(Boolean)
                .join(" ")}
              onMouseEnter={() => setHighlighted(index)}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => commit(opt)}
            >
              {opt}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
