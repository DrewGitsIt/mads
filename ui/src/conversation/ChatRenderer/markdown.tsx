import { useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { marked, type Links, type Token, type Tokens, type TokensList } from "marked";
import { Fold, useArrivals } from "../../components/Motion";
import DOMPurify from "dompurify";
import { useElectronClipboardBridge } from "../TerminalRenderer/ptyBridge";
import { getLinkOpener, needsDescribing } from "../../platform/links";
import { useLinkBase } from "./linkBase";
import styles from "./markdown.module.css";

marked.setOptions({ gfm: true, breaks: false });

const ALLOWED_TAGS = [
  "p",
  "br",
  "strong",
  "em",
  "del",
  "a",
  "ul",
  "ol",
  "li",
  "blockquote",
  "code",
  "pre",
  "h1",
  "h2",
  "h3",
  "h4",
  "hr",
  "table",
  "thead",
  "tbody",
  "tr",
  "th",
  "td",
];

/** Where a rendered link keeps the href its author wrote; the real href is inert. */
const LINK_HREF_ATTR = "data-mads-href";

/**
 * Takes every link out of the browser's hands. The href moves to
 * LINK_HREF_ATTR, untouched (a relative `src/foo.ts` must reach main as
 * written, not resolved against the app's origin), and the href itself
 * becomes "#", so no click, Cmd-click, middle-click or drag can navigate the
 * window or open the Router in a browser. Markdown's click handler opens the
 * stored href through main. The destination stays visible as the tooltip.
 */
function moveLinkHref(node: Element): void {
  if (node.tagName !== "A") return;
  const href = node.getAttribute("href");
  if (!href) {
    node.removeAttribute(LINK_HREF_ATTR);
    return;
  }
  node.setAttribute(LINK_HREF_ATTR, href);
  node.setAttribute("href", "#");
  if (!node.getAttribute("title")) node.setAttribute("title", href);
}

function sanitize(parsed: string): string {
  // Scoped to this call: DOMPurify hooks are global to its instance.
  DOMPurify.addHook("afterSanitizeAttributes", moveLinkHref);
  try {
    return DOMPurify.sanitize(parsed, {
      ALLOWED_TAGS,
      ALLOWED_ATTR: ["href", "title"],
      // No data-* from the source: LINK_HREF_ATTR is only ever ours.
      ALLOW_DATA_ATTR: false,
    });
  } finally {
    DOMPurify.removeHook("afterSanitizeAttributes");
  }
}

/** The rendered link a click landed on, if it is one of ours inside `root`. */
function clickedLink(event: MouseEvent<HTMLElement>): HTMLAnchorElement | undefined {
  const link = (event.target as Element | null)?.closest?.(`a[${LINK_HREF_ATTR}]`);
  return link instanceof HTMLAnchorElement && event.currentTarget.contains(link) ? link : undefined;
}

/** Marks a link whose file isn't on this machine, or that mads can't open, so it reads that way. */
function markLink(link: HTMLAnchorElement, state: string): void {
  const href = link.getAttribute(LINK_HREF_ATTR) ?? "";
  if (state === "missing") {
    link.dataset.linkState = "missing";
    link.title = `${href} — not found on this computer`;
  } else if (state === "none") {
    link.dataset.linkState = "none";
  } else {
    delete link.dataset.linkState;
    link.title = href;
  }
}

/**
 * Renders trusted-shape markdown (agent/user prose, session summaries) as
 * sanitized HTML. Transcript text can contain arbitrary markdown, so this
 * never hands parser output straight to dangerouslySetInnerHTML — DOMPurify
 * strips scripts/styles/event-handler attributes first (MADS-UX §3.3
 * Conversation tab / HANDOFF.md §7).
 *
 * Rendered one top-level block (paragraph, list, code block…) at a time,
 * each its own element: as a reply streams in, only the block still
 * growing changes, so React leaves every earlier block's DOM alone. That is
 * what lets a code block that arrives mid-reply fold in once instead of
 * being rebuilt (and its entrance restarted) with every revealed word.
 */
export function Markdown({
  text,
  className,
  foldNewCode = false,
  breaks = false,
}: {
  text: string;
  className?: string;
  /**
   * Every newline is a line break, not standard markdown's soft wrap. On for
   * all chat prose (user and agent bubbles, digests, notices, injected and
   * inbound messages, compaction summaries, subagent text): a Shift+Enter in
   * the composer reads as a new line in the history, and so does a newline
   * an agent wrote, so one rule holds for everything in the conversation.
   * Off for documents rendered elsewhere (task descriptions).
   */
  breaks?: boolean;
  /**
   * A paced reveal lands each code block whole: fold in the ones that
   * arrive after mount. Unpaced text grows a block line by line, which
   * moves on its own, so it never folds.
   */
  foldNewCode?: boolean;
}) {
  const blocks = useMemo(() => {
    // Breaks are decided at lex time (the inline tokenizer emits the <br>).
    const tokens = marked.lexer(text, { ...marked.defaults, breaks });
    // Reference-style link definitions are collected document-wide; each
    // block's parse needs the whole table. `breaks` rides in the key too: it
    // changes a block's tokens without changing its source.
    const links = tokens.links;
    const linksKey = JSON.stringify(links) + (breaks ? ":breaks" : "");
    return tokens
      .filter((t) => t.type !== "space")
      .map((t, i) => ({ key: `${i}:${t.type}`, token: t, links, linksKey }));
  }, [text, breaks]);
  const rootRef = useRef<HTMLDivElement>(null);
  const linkBase = useLinkBase();

  // Ask main which file links point nowhere on this machine, once per link
  // element (a block re-rendered while streaming brings fresh ones). A click
  // asks again, so a file the agent creates later still opens.
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const fresh = [...root.querySelectorAll<HTMLAnchorElement>(`a[${LINK_HREF_ATTR}]:not([data-link-checked])`)];
    const ask = fresh.filter((a) => needsDescribing(a.getAttribute(LINK_HREF_ATTR) ?? ""));
    fresh.forEach((a) => (a.dataset.linkChecked = ""));
    if (ask.length === 0) return;
    void getLinkOpener()
      .describe(
        ask.map((a) => a.getAttribute(LINK_HREF_ATTR) ?? ""),
        linkBase,
      )
      .then((states) => states.forEach((state, i) => ask[i] && markLink(ask[i], state)))
      .catch(() => undefined);
  }, [blocks, linkBase]);

  const openLink = (event: MouseEvent<HTMLDivElement>) => {
    const link = clickedLink(event);
    // Primary and middle button, any modifier (Cmd-click included): the
    // inert href must never act on its own. Right-click keeps its menu.
    if (!link || (event.button !== 0 && event.button !== 1)) return;
    event.preventDefault();
    const href = link.getAttribute(LINK_HREF_ATTR) ?? "";
    void getLinkOpener()
      .open(href, linkBase)
      .then((outcome) => {
        if (outcome === "missing" || outcome === "none") markLink(link, outcome);
        else if (outcome === "opened" || outcome === "revealed") markLink(link, "ok");
      })
      .catch(() => undefined);
  };

  const arrived = useArrivals(
    blocks.map((b) => b.key),
    foldNewCode,
  );

  return (
    <div
      ref={rootRef}
      className={[styles.markdown, className].filter(Boolean).join(" ")}
      onClick={openLink}
      onAuxClick={openLink}
    >
      {blocks.map(({ key, token, links, linksKey }) =>
        // Every code block sits in a fold, arriving or not, so the element
        // never changes type (and remounts) the render after it lands.
        token.type === "code" ? (
          <Fold key={key} appear={arrived(key)} fade className={styles.codeFold}>
            <CodeBlock token={token as Tokens.Code} links={links} linksKey={linksKey} />
          </Fold>
        ) : (
          <Block key={key} token={token} links={links} linksKey={linksKey} />
        ),
      )}
    </div>
  );
}

/** One top-level block, parsed and sanitized only when its source changes. */
function Block({ token, links, linksKey }: { token: Token; links: Links; linksKey: string }) {
  const html = useMemo(() => {
    const list = Object.assign([token], { links }) as TokensList;
    return sanitize(marked.parser(list));
    // The token object is new on every lex; its source and the link table are what matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token.raw, linksKey]);
  return <div className={styles.block} dangerouslySetInnerHTML={{ __html: html }} />;
}

/** How long "Copied" stays on the button — the same beat as the dock's path Copy. */
const COPIED_MS = 1500;

/**
 * A top-level fenced/indented code block with a Copy button in its corner,
 * shown on hover or focus. Copies the token's source text — the code as the
 * agent wrote it, not the rendered HTML's text. Code nested in a list or
 * quote renders inside its parent's Block and gets no button.
 */
function CodeBlock({ token, links, linksKey }: { token: Tokens.Code; links: Links; linksKey: string }) {
  const clipboard = useElectronClipboardBridge();
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(timer);
  }, [copied]);

  // Electron's clipboard bridge when present (navigator.clipboard is
  // unreliable in a BrowserWindow), else navigator.clipboard — WorkArea's split.
  function copy() {
    const write = clipboard
      ? clipboard.writeText(token.text)
      : navigator.clipboard?.writeText
        ? navigator.clipboard.writeText(token.text)
        : undefined;
    write?.then(
      () => setCopied(true),
      () => {},
    );
  }

  return (
    <div className={styles.code}>
      <Block token={token} links={links} linksKey={linksKey} />
      <button
        type="button"
        className={styles.copy}
        data-find-skip
        data-copied={copied || undefined}
        aria-label={copied ? "Copied" : "Copy code"}
        onClick={copy}
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
