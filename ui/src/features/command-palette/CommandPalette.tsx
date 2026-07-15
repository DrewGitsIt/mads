import { useEffect, useMemo, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { Panel, PanelBody, PanelHeader } from "../../components/Panel/Panel";
import { filterCommands, type Command } from "./commandSource";
import styles from "./CommandPalette.module.css";

export interface CommandPaletteProps {
  open: boolean;
  onClose: () => void;
  commands: Command[];
  onDispatch: (command: Command) => void;
}

/**
 * Presentational overlay only — the ⌘K hotkey listener lives in App.tsx, and
 * `commands` (built from already-subscribed caches) arrive as props, so
 * opening the palette issues no fetch of its own (HANDOFF.md §3).
 */
export function CommandPalette({ open, onClose, commands, onDispatch }: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);

  const filtered = useMemo(() => filterCommands(commands, query), [commands, query]);

  useEffect(() => {
    if (open) {
      setQuery("");
      setSelectedIndex(0);
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    function handleDocumentKeyDown(e: globalThis.KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", handleDocumentKeyDown);
    return () => document.removeEventListener("keydown", handleDocumentKeyDown);
  }, [open, onClose]);

  if (!open) return null;

  function dispatch(command: Command) {
    onDispatch(command);
    onClose();
  }

  function handleQueryChange(e: ChangeEvent<HTMLInputElement>) {
    setQuery(e.target.value);
    setSelectedIndex(0);
  }

  function handleInputKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSelectedIndex((i) => Math.min(i + 1, filtered.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSelectedIndex((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const command = filtered[selectedIndex];
      if (command) dispatch(command);
    }
  }

  return (
    <div className={styles.scrim} onClick={onClose}>
      <div className={styles.frame} onClick={(e) => e.stopPropagation()}>
        <Panel className={styles.panel}>
          <PanelHeader>
            <input
              autoFocus
              className={styles.input}
              placeholder="Jump to a session, start a pipeline, run retro…"
              value={query}
              onChange={handleQueryChange}
              onKeyDown={handleInputKeyDown}
            />
          </PanelHeader>
          <PanelBody>
            <div className={styles.results}>
              {filtered.length === 0 ? (
                <div className={styles.empty}>No matches</div>
              ) : (
                <ul className={styles.list}>
                  {filtered.map((command, index) => (
                    <li key={command.id}>
                      <button
                        type="button"
                        data-testid={command.id}
                        className={[styles.row, index === selectedIndex ? styles.selected : ""]
                          .filter(Boolean)
                          .join(" ")}
                        onMouseEnter={() => setSelectedIndex(index)}
                        onClick={() => dispatch(command)}
                      >
                        <span className={styles.label}>{command.label}</span>
                        {command.detail && <span className={styles.detail}>{command.detail}</span>}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </PanelBody>
        </Panel>
      </div>
    </div>
  );
}
