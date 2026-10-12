import { useEffect, useMemo, useState } from "react";
import { marked, type Links, type Token, type Tokens, type TokensList } from "marked";
import { Fold, useArrivals } from "../../components/Motion";
import DOMPurify from "dompurify";
import { useElectronClipboardBridge } from "../TerminalRenderer/ptyBridge";
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

function sanitize(parsed: string): string {
  return DOMPurify.sanitize(parsed, {
    ALLOWED_TAGS,
    ALLOWED_ATTR: ["href", "title"],
  });
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
}: {
  text: string;
  className?: string;
  /**
   * A paced reveal lands each code block whole: fold in the ones that
   * arrive after mount. Unpaced text grows a block line by line, which
   * moves on its own, so it never folds.
   */
  foldNewCode?: boolean;
}) {
  const blocks = useMemo(() => {
    const tokens = marked.lexer(text);
    // Reference-style link definitions are collected document-wide; each
    // block's parse needs the whole table.
    const links = tokens.links;
    const linksKey = JSON.stringify(links);
    return tokens
      .filter((t) => t.type !== "space")
      .map((t, i) => ({ key: `${i}:${t.type}`, token: t, links, linksKey }));
  }, [text]);
  const arrived = useArrivals(
    blocks.map((b) => b.key),
    foldNewCode,
  );

  return (
    <div className={[styles.markdown, className].filter(Boolean).join(" ")}>
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
