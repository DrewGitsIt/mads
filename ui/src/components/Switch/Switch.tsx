import type { ButtonHTMLAttributes } from "react";
import styles from "./Switch.module.css";

export interface SwitchProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "onChange" | "role" | "type" | "children"> {
  checked: boolean;
  onChange: (checked: boolean) => void;
}

/**
 * An on/off setting that takes effect as a state, not an action — a themed
 * stand-in for a checkbox. A real <button role="switch">, so an external
 * <label htmlFor> names it and clicking that label flips it; pass aria-label
 * when there's no visible label.
 */
export function Switch({ checked, onChange, className, ...rest }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      {...rest}
      className={[styles.switch, className].filter(Boolean).join(" ")}
      onClick={() => onChange(!checked)}
    />
  );
}
