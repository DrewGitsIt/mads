/**
 * Word units for paced reveal: how much of a text to reveal for a budget of
 * words, holding back a word still streaming in and revealing a fenced code
 * block whole (nobody reads code at prose pace).
 */

// Word segmentation: Intl.Segmenter knows where words end in scripts that
// don't separate them with spaces (Chinese, Japanese, Thai); without it,
// whitespace-separated runs stand in.
const segmenter =
  typeof Intl !== "undefined" && "Segmenter" in Intl ? new Intl.Segmenter(undefined, { granularity: "word" }) : null;

// How far ahead of the reveal point one word lookup scans. A word longer
// than this (a URL, a hash) reveals in window-sized pieces.
const WINDOW = 256;

/**
 * End offset of the next word unit in `text` from `pos`: any leading
 * whitespace/punctuation, the word itself, and punctuation glued to its end
 * ("word," not "word" then ", next"). undefined when no word starts inside
 * the window.
 */
function nextWordEnd(text: string, pos: number): number | undefined {
  const chunk = text.slice(pos, pos + WINDOW);
  let end: number | undefined;
  if (segmenter) {
    for (const seg of segmenter.segment(chunk)) {
      if (end === undefined) {
        if (seg.isWordLike) end = seg.index + seg.segment.length;
        continue;
      }
      // Glue trailing punctuation, but stop at whitespace.
      if (seg.isWordLike || /^\s/.test(seg.segment)) break;
      end = seg.index + seg.segment.length;
    }
  } else {
    const m = /^\s*\S+/.exec(chunk);
    if (m) end = m[0].length;
  }
  return end === undefined ? undefined : pos + end;
}

const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})[^\n]*(\n|$)/;

/**
 * If a fenced code block opens at `pos` (a line start), the end offset of
 * the whole block through its closing fence line — or undefined when the
 * closing fence hasn't arrived yet. Not a block at all → null.
 */
function fenceEnd(text: string, pos: number): number | undefined | null {
  const open = FENCE_OPEN.exec(text.slice(pos));
  if (!open) return null;
  const marker = open[1];
  if (!open[2]) return undefined; // the opening line itself is still arriving
  let from = pos + open[0].length;
  for (;;) {
    const nl = text.indexOf("\n", from);
    const line = text.slice(from, nl === -1 ? text.length : nl);
    const close = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(line);
    if (close && close[1][0] === marker[0] && close[1].length >= marker.length) {
      // A closing line still arriving could yet grow past a bare fence.
      if (nl === -1) return undefined;
      return nl + 1;
    }
    if (nl === -1) return undefined;
    from = nl + 1;
  }
}

/**
 * How much of `text` to reveal for up to `words` word units. `atLineStart`
 * says whether `text` begins a line (what precedes it was empty or ended in
 * a newline). `final` means no more text will join this run — the turn is
 * finishing, or a tool card follows — so a word touching the end of the
 * buffer is whole; otherwise it may be half of a word still streaming in,
 * and is held back.
 */
export function takeWords(
  text: string,
  words: number,
  atLineStart: boolean,
  final: boolean,
): { end: number; used: number } {
  let pos = 0;
  let used = 0;
  while (used < words && pos < text.length) {
    const lineStart = pos === 0 ? atLineStart : text[pos - 1] === "\n";
    // Blank-line remainders are free: they carry no words, and consuming
    // them puts the next fence check at a real line start.
    const blank = /^[ \t]*\n/.exec(text.slice(pos));
    if (blank) {
      pos += blank[0].length;
      continue;
    }
    if (lineStart) {
      const fence = fenceEnd(text, pos);
      if (fence !== null) {
        if (fence === undefined) {
          if (final) return { end: text.length, used: used + 1 };
          break; // wait for the closing fence
        }
        pos = fence;
        used++;
        continue;
      }
    }
    const end = nextWordEnd(text, pos);
    if (end === undefined) {
      if (pos + WINDOW < text.length) {
        pos += WINDOW; // a long run with no word in it: reveal a window
        used++;
        continue;
      }
      if (final) pos = text.length;
      break;
    }
    if (end >= text.length && !final) break;
    pos = end;
    used++;
  }
  return { end: pos, used };
}

