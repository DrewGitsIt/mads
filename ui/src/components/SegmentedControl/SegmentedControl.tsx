import { useRef, type KeyboardEvent } from "react";
import styles from "./SegmentedControl.module.css";

export interface SegmentedOption<T extends string = string> {
  value: T;
  label: string;
}

export interface SegmentedControlProps<T extends string = string> {
  value: T;
  options: SegmentedOption<T>[];
  onChange: (value: T) => void;
  disabled?: boolean;
  id?: string;
  /** Name the group by its visible label's id (preferred) or a string. */
  "aria-labelledby"?: string;
  "aria-label"?: string;
  className?: string;
}

/**
 * A short, closed set of choices shown side by side — the theme, a mode, a
 * tier. Radio-group semantics: one tab stop (the chosen option), arrow keys
 * move the choice and focus together, Home/End jump to the ends. When `value`
 * matches no option, the first one takes the tab stop and none reads checked.
 */
export function SegmentedControl<T extends string = string>({
  value,
  options,
  onChange,
  disabled,
  id,
  className,
  ...aria
}: SegmentedControlProps<T>) {
  const buttonsRef = useRef<(HTMLButtonElement | null)[]>([]);
  const selectedIndex = options.findIndex((o) => o.value === value);
  const tabStop = Math.max(selectedIndex, 0);

  function choose(index: number) {
    const opt = options[index];
    if (!opt) return;
    buttonsRef.current[index]?.focus();
    if (opt.value !== value) onChange(opt.value);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number) {
    const last = options.length - 1;
    let next: number | null = null;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = index === last ? 0 : index + 1;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = index === 0 ? last : index - 1;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = last;
    if (next === null) return;
    e.preventDefault();
    choose(next);
  }

  return (
    <div
      id={id}
      role="radiogroup"
      aria-disabled={disabled || undefined}
      className={[styles.group, className].filter(Boolean).join(" ")}
      {...aria}
    >
      {options.map((opt, index) => (
        <button
          key={opt.value}
          ref={(el) => {
            buttonsRef.current[index] = el;
          }}
          type="button"
          role="radio"
          aria-checked={opt.value === value}
          tabIndex={index === tabStop ? 0 : -1}
          disabled={disabled}
          className={styles.option}
          onClick={() => choose(index)}
          onKeyDown={(e) => handleKeyDown(e, index)}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}
