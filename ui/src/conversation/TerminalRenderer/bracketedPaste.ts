/**
 * Standard xterm/bash bracketed-paste convention — see HANDOFF.md "PTY
 * bracketed paste format". The terminal compose sheet sends its text through
 * this, so a multi-line prompt arrives in the TUI as one paste rather than
 * as typed lines (each newline would otherwise submit).
 */
export function formatBracketedPaste(text: string): string {
  return `\x1b[200~${text}\x1b[201~`;
}
