# Link Mode Flow (Option C) — sessions-ux.html Extension

## Overview
Extend `docs/mockups/sessions-ux.html` with a new section showing the three-step flow for creating linked sessions via Option C's slide-in link icon. This mockup should render the real link model from `railLinks.ts` and dialog structure from `LinkChooserDialog.tsx`.

## Three-Step Flow Reference

### Step 1: Link Mode Initiated
**What happens:** User clicks the link (🔗) icon on a card. That card becomes the anchor (highlighted). Other same-workspace cards show tap-to-add checkboxes. Cross-workspace cards are dimmed and tagged "different workspace". A floating action bar at the bottom shows the count of selected sessions and a "Next" button.

**Real behavior reference:** User selects 2+ sessions to link; sidebar must enforce workspace boundary (no cross-workspace links per LinkChooserDialog constraint).

**Mock session data for this step:**
- Anchor: "Fix deploy on Ubuntu runners" (workspace: main)
- Selectable: "Session rail readability pass" (workspace: main) — gets checkbox
- Selectable: "Mergiraf land-queue driver" (workspace: main) — gets checkbox  
- Dimmed: (create a dummy session in a different workspace) — gets "different workspace" tag

**Key UI elements:**
- Anchor card: thick border (use --accent), maybe a small "Anchor" badge/label
- Checkbox styling: use standard `<input type="checkbox">` with `accent-color: var(--accent)`
- Dimmed cards: `opacity: 0.5` or similar, with a small tag in top-right reading "different workspace"
- Floating bar: fixed to bottom of the panel, showing "Selected: 2 sessions" and a "Next →" button (uses --accent colors)

### Step 2: Mode + Holder Picker Dialog
**What happens:** User clicks "Next" and sees a dialog (or panel) showing two preset cards (Monitor / Leader), each with a description and grant chips. Below that, a radio-group asks "Who leads?" or "Who monitors?" (text changes based on preset choice). Each radio option shows the holder name and a fan-out arrow (→) listing the subjects.

**Real behavior reference:** See `LinkChooserDialog.tsx`:
- Two presets: Monitor (read), Leader (orchestrate)
- PRESET_COPY data:
  - Monitor: "Follows along: receives a digest after each watched agent's turn. Never speaks into them."
  - Leader: "Gets a tool to message and direct the other agents. Its reach is capped by each session's own trust level."
- PRESET_GRANTS:
  - monitor: ["read"]
  - leader: ["orchestrate"]
- Hand-raiser (inject) is NOT offered to users; system-only
- Holder picker always defaults to the first selected session (`sessionIds[0]`)
- Radio label format: `<name> → <comma-joined list of other names>`

**Key UI elements:**
- Preset card buttons: `.preset-card` styling (similar to pill but larger, selectable)
- Grant chips inside each preset: `.grant-chip` with text content "read" / "orchestrate"
- Radio group label: "Who leads?" (if leader preset) or "Who monitors?" (if monitor preset)
- Each radio option: `<label><input type="radio" name="holder" /><b>Session name</b><span class="fan-out">→ subject1, subject2</span></label>`
- Constraint note (verbatim from LinkChooserDialog.tsx or adapted):
  ```
  One leader per subject — a session already led by someone else is refused. 
  Links never cross different worktrees of the same repo. 
  Pipeline runs can be monitored, not led. 
  (Hand-raiser isn't offered here: it's granted automatically to a session's own agent 
  while its pipeline runs, and it talks to systems, not agents.)
  ```

### Step 3: Resulting Rail Display
**What happens:** After confirmation, the rail re-renders showing:
- The holder session card (e.g., "Fix deploy on Ubuntu runners") with a badge (e.g., "LEADER · 1" or "MONITOR · 2")
- Nested below the holder: each subject session as an indented row, connected by a vertical line, each labeled "LINKED" (if holder leads) or "WATCHED" (if holder monitors)
- On hover of a subject row: an unlink X button appears on the right

**Real behavior reference:** From `railLinks.ts`:
- `linkRole(link)` → "leader" if grants include orchestrate, "monitor" if grants include read
- `computeRailLinks()` builds the graph:
  - `badge` format: "LEADER" (1), "LEADER · N" (N>1), "MONITOR", "MONITOR · N"
  - For each subject under a holder: `badge` is "LINKED" if holder has leader role, else "WATCHED"
- `buildRailRows()` nests subjects under holders; subjects are removed from the flat list

**Mock data for step 3:**
- Holder card: "Fix deploy on Ubuntu runners" 
  - Badge: "LEADER · 2" (because it holds leader role over 2 subjects)
  - Subjects:
    - "Session rail readability pass" — LINKED badge (because holder is leader)
    - "Mergiraf land-queue driver" — LINKED badge
- A second holder if desired (e.g., "Profile optimization" as MONITOR with 1 watched subject)

**Key UI elements:**
- Holder card: `.rail-holder-badge` containing the badge text (e.g., "LEADER · 2")
- Subject row: `.subject-nested`, indented (maybe `padding-left: 32px`)
- Connector line: `.subject-connector` — a vertical line on the left side, drawn with a `::before` pseudo-element or a thin `border-left`
- Subject badge: `.subject-badge` with text "LINKED" or "WATCHED" (styled like a small chip)
- Unlink button: `.unlink-hover-x` (opacity: 0 at rest, opacity: 1 on `.subject-nested:hover`), positioned on the right

## CSS Classes to Add

```css
/* Step 1: Link mode entry */
.link-step { /* container for each of the 3 panels */ }
.link-anchor { /* the anchor card's visual treatment */ border: 2px solid var(--accent); }
.link-checkbox { /* styling for checkboxes in step 1 */ }
.link-dimmed { /* cross-workspace cards */ opacity: 0.5; position: relative; }
.link-dimmed::after { content: "different workspace"; /* or a tag element */ }
.link-floating-bar { /* bottom bar with selection count and Next button */ }

/* Step 2: Preset + holder picker */
.preset-card { /* Monitor/Leader button */ cursor: pointer; padding: var(--space-3); border: 1px solid var(--line-soft); border-radius: var(--r); }
.preset-card[aria-selected="true"] { border-color: var(--accent); background: var(--accent-soft); }
.grant-chip { display: inline-block; background: var(--bg3); border: 1px solid var(--line); border-radius: 12px; padding: 2px 8px; margin-right: 4px; font-size: var(--fs-chip); }
.holder-picker { /* radio group styling */ }
.holder-picker label { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2); cursor: pointer; }
.holder-picker input[type="radio"] { cursor: pointer; }
.fan-out-arrow { color: var(--text3); margin-left: auto; }

/* Step 3: Resulting rail */
.rail-holder-badge { display: inline-block; background: var(--accent-soft); color: var(--accent); padding: 2px 8px; border-radius: 4px; font-size: var(--fs-chip); font-weight: 700; }
.subject-nested { padding-left: 32px; position: relative; margin-top: 4px; }
.subject-nested::before { /* connector line */ content: ""; position: absolute; left: 10px; top: -12px; width: 1px; height: calc(100% + 12px); background: var(--line-soft); }
.subject-badge { display: inline-block; background: var(--info-soft); color: var(--info-fg); padding: 2px 6px; border-radius: 3px; font-size: var(--fs-chip); font-weight: 600; margin-left: var(--space-2); }
.unlink-hover-x { opacity: 0; transition: opacity 0.12s; cursor: pointer; }
.subject-nested:hover .unlink-hover-x { opacity: 1; }
```

## HTML Structure Sketch

```html
<!-- NEW SECTION 3 -->
<section class="block">
  <h2>
    <span class="tag">#3</span> Link mode flow — Option C three-step experience
    <span class="sub">create leader/monitor relationships between sessions</span>
  </h2>
  <div class="body">
    <p class="note"><b>How it works:</b> click the link icon (🔗) on a card → select other sessions in the same workspace → choose a preset (Monitor or Leader) and confirm who leads → subjects appear nested under their holder in the rail.</p>
    
    <div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-4); margin-top: var(--space-4);">
    
      <!-- STEP 1 -->
      <div class="link-step">
        <div class="rowlabel">Step 1 · Link mode initiated</div>
        <p class="note" style="margin-bottom: var(--space-3);">Click the 🔗 icon. The card becomes the anchor; same-workspace cards show checkboxes; cross-workspace cards dim. Floating bar tracks selection.</p>
        <div class="railpanel" style="position: relative; min-height: 300px;">
          <div class="grouphd">Active <span>· 3</span></div>
          
          <!-- Anchor card -->
          <div class="scard link-anchor" style="border: 2px solid var(--accent);">
            <div class="top"><span class="dot on"></span><span class="nm">Fix deploy on Ubuntu runners</span></div>
            <div style="margin-top: 4px; font-size: var(--fs-chip); color: var(--accent); font-weight: 600;">Anchor</div>
          </div>
          
          <!-- Same-workspace selectable -->
          <div class="scard">
            <div style="display: flex; align-items: center; gap: var(--space-2);">
              <input type="checkbox" style="accent-color: var(--accent);" />
              <span class="dot on"></span>
              <span class="nm">Session rail readability pass</span>
            </div>
          </div>
          
          <div class="scard">
            <div style="display: flex; align-items: center; gap: var(--space-2);">
              <input type="checkbox" style="accent-color: var(--accent);" />
              <span class="dot on"></span>
              <span class="nm">Mergiraf land-queue driver</span>
            </div>
          </div>
          
          <!-- Cross-workspace dimmed -->
          <div class="scard" style="opacity: 0.5;">
            <div style="display: flex; align-items: center; gap: var(--space-2); justify-content: space-between;">
              <div style="display: flex; align-items: center; gap: var(--space-2);">
                <span class="dot on"></span>
                <span class="nm">Another workspace's session</span>
              </div>
              <span style="font-size: var(--fs-chip); color: var(--text3); background: var(--bg3); padding: 2px 6px; border-radius: 3px;">different workspace</span>
            </div>
          </div>
          
          <!-- Floating bar -->
          <div style="position: absolute; bottom: 0; left: 0; right: 0; padding: var(--space-3); background: var(--accent-soft); border-top: 1px solid var(--line); display: flex; align-items: center; justify-content: space-between; gap: var(--space-2);">
            <div style="color: var(--accent); font-weight: 600;">Selected: 2 sessions</div>
            <button class="btn btn-primary">Next →</button>
          </div>
        </div>
      </div>
      
      <!-- STEP 2 -->
      <div class="link-step">
        <div class="rowlabel">Step 2 · Choose mode & holder</div>
        <p class="note" style="margin-bottom: var(--space-3);">Pick a preset. Then pick which session holds that role — all others become subjects.</p>
        
        <!-- Preset chooser -->
        <div style="display: flex; flex-direction: column; gap: var(--space-2); margin-bottom: var(--space-3);">
          <!-- Leader preset -->
          <button class="preset-card" aria-selected="true" style="border-color: var(--accent); background: var(--accent-soft); cursor: pointer; padding: var(--space-3); text-align: left;">
            <div style="font-weight: 600; color: var(--text); margin-bottom: 4px;">Leader</div>
            <div style="font-size: var(--fs-sm); color: var(--text2); margin-bottom: 6px;">Gets a tool to message and direct the other agents. Its reach is capped by each session's own trust level.</div>
            <div style="display: flex; gap: 4px;">
              <span class="grant-chip">orchestrate</span>
            </div>
          </button>
          
          <!-- Monitor preset -->
          <button class="preset-card" style="cursor: pointer; padding: var(--space-3); text-align: left;">
            <div style="font-weight: 600; color: var(--text); margin-bottom: 4px;">Monitor</div>
            <div style="font-size: var(--fs-sm); color: var(--text2); margin-bottom: 6px;">Follows along: receives a digest after each watched agent's turn. Never speaks into them.</div>
            <div style="display: flex; gap: 4px;">
              <span class="grant-chip">read</span>
            </div>
          </button>
        </div>
        
        <!-- Holder picker -->
        <div style="border-top: 1px solid var(--line-soft); padding-top: var(--space-3);">
          <div style="font-size: var(--fs-sm); font-weight: 600; margin-bottom: var(--space-2); color: var(--text3);">Who leads?</div>
          <label style="display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2); margin-bottom: 4px; cursor: pointer; border-radius: var(--r-sm); border: 1px solid transparent;">
            <input type="radio" name="holder" checked style="accent-color: var(--accent);" />
            <span style="flex: 1;"><b>Fix deploy on Ubuntu runners</b></span>
            <span style="color: var(--text3); font-size: var(--fs-sm);">→ Session rail readability pass, Mergiraf land-queue driver</span>
          </label>
          <label style="display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2); cursor: pointer; border-radius: var(--r-sm); border: 1px solid transparent;">
            <input type="radio" name="holder" style="accent-color: var(--accent);" />
            <span style="flex: 1;"><b>Session rail readability pass</b></span>
            <span style="color: var(--text3); font-size: var(--fs-sm);">→ Fix deploy on Ubuntu runners, Mergiraf land-queue driver</span>
          </label>
        </div>
        
        <!-- Constraint note -->
        <div style="margin-top: var(--space-3); padding: var(--space-2); background: var(--bg2); border-radius: var(--r-sm); border-left: 2px solid var(--line); font-size: var(--fs-sm); color: var(--text3); line-height: 1.5;">
          <b style="color: var(--text2);">Constraints:</b> One leader per subject. Links never cross different worktrees. Pipeline runs can be monitored, not led.
        </div>
      </div>
      
      <!-- STEP 3 -->
      <div class="link-step">
        <div class="rowlabel">Step 3 · Resulting rail</div>
        <p class="note" style="margin-bottom: var(--space-3);">The holder card shows a badge; subjects nest below with LINKED/WATCHED badges. Hover to unlink.</p>
        <div class="railpanel">
          <div class="grouphd">Active <span>· 1</span></div>
          
          <!-- Holder card with badge -->
          <div class="scard">
            <div class="top">
              <span class="dot on"></span>
              <span class="nm">Fix deploy on Ubuntu runners</span>
              <span style="margin-left: auto; display: inline-block; background: var(--accent-soft); color: var(--accent); padding: 2px 8px; border-radius: 4px; font-size: var(--fs-chip); font-weight: 700;">LEADER · 2</span>
            </div>
          </div>
          
          <!-- Nested subject 1 -->
          <div class="scard" style="margin-left: 24px; margin-top: 4px; position: relative;">
            <div style="position: absolute; left: -18px; top: -6px; width: 1px; height: 30px; background: var(--line-soft);"></div>
            <div style="display: flex; align-items: center; justify-content: space-between;">
              <div style="display: flex; align-items: center; gap: var(--space-2);">
                <span class="dot on"></span>
                <span class="nm">Session rail readability pass</span>
                <span style="display: inline-block; background: var(--info-soft); color: var(--info-fg); padding: 2px 6px; border-radius: 3px; font-size: var(--fs-chip); font-weight: 600;">LINKED</span>
              </div>
              <button style="opacity: 0; cursor: pointer; color: var(--bad-fg); font-size: 12px; transition: opacity 0.12s;" title="Unlink">✕</button>
            </div>
          </div>
          
          <!-- Nested subject 2 -->
          <div class="scard" style="margin-left: 24px; position: relative;">
            <div style="position: absolute; left: -18px; top: -6px; width: 1px; height: 30px; background: var(--line-soft);"></div>
            <div style="display: flex; align-items: center; justify-content: space-between;">
              <div style="display: flex; align-items: center; gap: var(--space-2);">
                <span class="dot on"></span>
                <span class="nm">Mergiraf land-queue driver</span>
                <span style="display: inline-block; background: var(--info-soft); color: var(--info-fg); padding: 2px 6px; border-radius: 3px; font-size: var(--fs-chip); font-weight: 600;">LINKED</span>
              </div>
              <button style="opacity: 0; cursor: pointer; color: var(--bad-fg); font-size: 12px; transition: opacity 0.12s;" title="Unlink">✕</button>
            </div>
          </div>
        </div>
      </div>
      
    </div>
  </div>
</section>
```

## Verification Checklist (for Executor)

- [ ] All three sections render side-by-side in a 3-column grid (parity with rail options #2)
- [ ] Tokens used match existing CSS variables (--bg*, --text*, --accent*, --line*, --info*, etc.)
- [ ] Step 1: anchor card has 2px border-accent, checkboxes use accent-color, dimmed card shows with opacity ~0.5 and "different workspace" tag
- [ ] Step 1: floating bar sticks to bottom, shows selection count and clickable "Next →" button
- [ ] Step 2: preset cards are clickable, selected one shows --accent-soft background and --accent border
- [ ] Step 2: grant chips display inline, styled as small badges
- [ ] Step 2: radio holder picker shows each session name and fan-out → arrow
- [ ] Step 2: constraint note reads as a distinct block with left border
- [ ] Step 3: holder card has inline badge (LEADER · 2, etc.)
- [ ] Step 3: subject rows are indented and have a vertical connector line on the left
- [ ] Step 3: subject badges show LINKED or WATCHED with --info-soft background
- [ ] Step 3: unlink X button is hidden at rest, visible on `.scard:hover`
- [ ] Entire mockup opens standalone in a browser without errors
- [ ] Font sizes, line-heights, and spacing use existing CSS variables consistently

## Known Risks & Decisions

1. **Connector line rendering**: Using `::before` pseudo-element with `position: absolute; left: -18px;` is hacky but works in static mockup. For real UI, may want to use a small SVG or a smarter layout (e.g., flex with custom line drawing).

2. **Cross-workspace dimming**: Mockup uses `opacity: 0.5`, which makes content hard to read. Real design may want a separate "disabled" state or visual treatment; consider consulting with product before finalizing.

3. **Floating bar positioning**: Step 1 uses `position: absolute; bottom: 0;` within the panel. In real app, must be careful not to cover content; consider a sticky position or a separate modal/sidebar UX.

4. **Font consistency**: All text should use existing --fs-* and --font variables; no new fonts introduced. Verify mockup renders correctly in the same browser/OS where the real UI will run.

5. **Radio picker UX**: The holder radio picker is text-heavy; in a real narrow UI, the fan-out list might wrap or need truncation. Mockup shows full text; real implementation should decide on responsive behavior.

