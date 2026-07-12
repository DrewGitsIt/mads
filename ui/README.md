# ui/

The Electron + TypeScript desktop app lands here in wave 6 (docs/MADS-WORK-ITEMS.md
W6.1–W6.11). Until then, the UI's source of truth is:

- `docs/MADS-UX.md` — information architecture, design-system rules, per-surface data
  contracts
- `docs/ux-prototype.html` — the approved clickable prototype (mockups 1–9, with design
  notes toggled via the "Design notes" control)

The UI talks only to the Router (HTTP + SSE) — never to other services directly.
