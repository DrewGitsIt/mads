# Chat reading runway

Mock: [`docs/mockups/chat-reading-runway.html`](mockups/chat-reading-runway.html) (live, side by side: today / glide / runway).

## Problem

With a paced reveal (Settings → Appearance → Reply streaming), every wrapped line grows the stream by one line height. `useStickToBottom` used to pin `scrollTop` to the bottom on every growth, with no easing, so the whole history jumped by a line every few seconds under the text being read. In the mock's sample reply that was 28 separate jumps.

## Behavior

- **Runway.** When a user-role message arrives (a human send, or a message from a linked mads session; an agent-started turn does not count), the stream scrolls so that message sits at the top. Below it the turn reserves the rest of the screen. The reply fills that space and nothing above it moves.
- **Page turn.** When the text reaches the bottom edge (the top of the pinned cluster, not the scroller's edge), one smooth scroll brings the last ~3 lines near the top, opening a new page of runway. The same reply in the mock: 2 moves.
- **Scroll-up hands over control.** Scrolling up, or ⌘↑, lets go as before. Getting back to the bottom (scrolling, ⌘↓) lands exactly where an uninterrupted reader would be: the same page, still turning pages.
- **Trim.** Once the turn is over, reserved space below the viewport is removed as soon as it is off screen. Removing what nobody can see moves nothing.
- **Settle.** Once the turn is over, the first keystroke in the composer (or focusing it after clicking away) collapses the leftover space; the reply slides down to sit above the composer.
- **Reduce motion** (the OS setting or mads' own): moves are instant, and there is **no settle**; the text stays where it is and trim handles the space.
- **Opening a session** whose last turn is over shows it settled (no blank). A turn still in progress opens on its runway.
- **Other scrollers** (DelegateView, AgentFeedView) keep line-by-line following; the runway is opt-in.

## Design

The runway is a spacer row at the end of the stream (just above the pinned cluster) whose height is chosen so that **the scroller's bottom is the runway position**. The lock keeps doing what it always did, pinning to the bottom, letting go on a scroll up, and re-engaging at the bottom, and just pins to a smarter bottom. The runway never writes `scrollTop`.

| Piece | Owns |
|---|---|
| `conversation/runway.ts` | Pure geometry: the spacer height for an anchor, the reply's end, the usable height and an optional cap. No DOM. |
| `conversation/useStickToBottom.ts` | The only writer of `scrollTop`. Mode is one explicit value, `follow` / `free` / `held`, shown as `data-stick` on the scroller. Opt-in `smooth`: a pin of more than ~2 lines scrolls smoothly (instant under reduce motion, and on the first pin after mount). Opt-in `beforePin`: called before every pin, so the spacer is sized before anything moves and the two can't race. |
| `conversation/useRunway.ts` | Finds the anchor (the last `[data-runway-anchor]`), measures, writes the spacer height, and holds the cap that trim and settle lower. Shows `data-pages` / `data-cap` on the spacer. |

### The geometry

All in the stream's content coordinates:

```
A      = anchor top − 12px gap
T      = the reply's end (the spacer's top)
U      = usable height = clientHeight − everything after the spacer (pinned cluster, padding)
page   = max(U − 64px, U / 2)           (64px ≈ 3 lines of context), at the moment of a turn
turns  = 0 if T ≤ A + O + U − 8, else ceil((T − (A + O + U − 8)) / page)
O      = offset: the sum of every turn's page, each as it was when it turned (0 at the anchor)
floor  = A + O + U                      (lowered to `cap` once trimmed or settled)
spacer = max(0, floor − T)
```

At the bottom, `scrollTop = A + O`, which doesn't depend on `U`, so a composer growing a line or the pinned cluster changing size moves nothing. Turns are kept, not recomputed from position: when the page size was `U`'s function on every pin, a running subagent's row (shown in the cluster while its card is off screen) changed `U`, moved the text by `pages × Δ`, brought its card back on screen, hid, and moved it off again — every frame (2026-11-10). Turns only add, so the runway also holds when the reply above shrinks; trim takes the leftover space once the turn is over.

### Known limit

At Native speed, a burst longer than a page arrives in one go and lands on its last page (with 3 lines of context), skipping the middle, much like today's jump to the tail. Paced speeds don't hit this, because the reveal is the reading clock. Check against Opus at Native, then decide whether it needs a rule.
