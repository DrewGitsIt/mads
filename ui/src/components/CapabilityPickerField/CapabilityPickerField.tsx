import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Chip } from "../Chip/Chip";
import { Popover } from "../Popover/Popover";
import type { Capability, CapabilityType } from "../../router/types";
import styles from "./CapabilityPickerField.module.css";

export interface CapabilityPickerFieldProps {
  label: string;
  type?: CapabilityType;
  capabilities: Capability[];
  value: string[];
  onChange: (names: string[]) => void;
}

/**
 * Registry-backed multi-select for Skills/MCP servers — constrains selection to
 * Capability.name values instead of free text, closing the phantom-reference
 * hole at the UI edge that agentgen validation closes server-side.
 */
export function CapabilityPickerField({ label, type, capabilities, value, onChange }: CapabilityPickerFieldProps) {
  const [pickerOpen, setPickerOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const registry = useMemo(
    () => (type ? capabilities.filter((c) => c.type === type) : capabilities),
    [capabilities, type],
  );

  const candidates = useMemo(
    () =>
      registry.filter(
        (c) => !value.includes(c.name) && c.name.toLowerCase().includes(query.trim().toLowerCase()),
      ),
    [registry, value, query],
  );

  useEffect(() => {
    setHighlightedIndex((i) => Math.min(i, Math.max(candidates.length - 1, 0)));
  }, [candidates.length]);

  function openPicker() {
    setPickerOpen(true);
    setQuery("");
    setHighlightedIndex(0);
    requestAnimationFrame(() => inputRef.current?.focus());
  }

  function add(name: string) {
    onChange([...value, name]);
    inputRef.current?.focus();
  }

  function remove(name: string) {
    onChange(value.filter((n) => n !== name));
  }

  function handleInputKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlightedIndex((i) => Math.min(i + 1, candidates.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlightedIndex((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const candidate = candidates[highlightedIndex];
      if (candidate) add(candidate.name);
    }
  }

  return (
    <div className={styles.field}>
      <span className={styles.label}>{label}</span>
      {value.length > 0 && (
        <div className={styles.chips}>
          {value.map((name) => (
            <Chip key={name} tone="neutral" onClick={() => remove(name)}>
              {name} ×
            </Chip>
          ))}
        </div>
      )}
      {registry.length === 0 ? (
        <div className={styles.empty}>
          No {label.toLowerCase()} registered yet. Add one from Library → Capabilities, then it'll show up here.
        </div>
      ) : (
        <Popover
          open={pickerOpen}
          onClose={() => setPickerOpen(false)}
          anchor={
            !pickerOpen && (
              <button type="button" className={styles.addbtn} onClick={openPicker}>
                + Add {label.toLowerCase()}…
              </button>
            )
          }
        >
          <div className={styles.pickerBox}>
            <input
              ref={inputRef}
              className={styles.pickerInput}
              placeholder={`Search ${label.toLowerCase()}…`}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={handleInputKeyDown}
            />
            <div className={styles.cands}>
              {candidates.length === 0 ? (
                <div className={styles.noResults}>No matches</div>
              ) : (
                candidates.map((candidate, index) => (
                  <div
                    key={candidate.id}
                    className={[styles.cand, index === highlightedIndex && styles.candHighlighted]
                      .filter(Boolean)
                      .join(" ")}
                    onMouseEnter={() => setHighlightedIndex(index)}
                    onClick={() => add(candidate.name)}
                  >
                    <span className={styles.candName}>{candidate.name}</span>
                    {candidate.description && <span className={styles.candDesc}>{candidate.description}</span>}
                  </div>
                ))
              )}
            </div>
            <div className={styles.pickhint}>↑↓ to move · Enter to add · Esc to close</div>
          </div>
        </Popover>
      )}
    </div>
  );
}
