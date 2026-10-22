import { findAll } from "./findText";

/** Text under these is chrome, not conversation: timestamps, Copy, Retry, card actions. */
const SKIP = "[data-find-skip], time";

/**
 * Ranges for every match of `pattern` in the text `el` renders. Matching
 * runs over the element's text nodes joined end to end, so a match may span
 * nodes (a word half in **bold**); each match maps back to a Range. A <br>
 * reads as the newline it stands for, so text across a line break matches
 * the way it reads (and as the transcript's own text does), not run
 * together. A plain
 * walk rather than a TreeWalker: it skips chrome a whole subtree at a time,
 * and behaves the same in every DOM the tests run.
 */
export function domMatches(el: Element, pattern: RegExp): Range[] {
  const nodes: Text[] = [];
  const starts: number[] = [];
  let text = "";
  const walk = (node: Node) => {
    for (let n = node.firstChild; n; n = n.nextSibling) {
      if (n.nodeType === Node.TEXT_NODE) {
        nodes.push(n as Text);
        starts.push(text.length);
        text += n.nodeValue ?? "";
      } else if (n.nodeType === Node.ELEMENT_NODE && (n as Element).tagName === "BR") {
        text += "\n";
      } else if (n.nodeType === Node.ELEMENT_NODE && !(n as Element).matches(SKIP)) {
        walk(n);
      }
    }
  };
  walk(el);
  if (nodes.length === 0) return [];

  // The node holding offset `at`, and the offset within it. `end` resolves
  // a boundary to the node it closes rather than the one it opens. A <br>'s
  // newline has no text node of its own: a boundary on it clamps to the
  // nearest end of a neighbouring node.
  let i = 0;
  const locate = (at: number, end: boolean): [Text, number] => {
    while (i < nodes.length - 1 && (end ? starts[i + 1] < at : starts[i + 1] <= at)) i++;
    const length = nodes[i].nodeValue?.length ?? 0;
    return [nodes[i], Math.min(Math.max(at - starts[i], 0), length)];
  };
  return findAll(text, pattern).map(({ start, length }) => {
    const range = el.ownerDocument.createRange();
    range.setStart(...locate(start, false));
    range.setEnd(...locate(start + length, true));
    return range;
  });
}
