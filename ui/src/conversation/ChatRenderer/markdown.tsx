import { useMemo } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";

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

/**
 * Renders trusted-shape markdown (agent/user prose, session summaries) as
 * sanitized HTML. Transcript text can contain arbitrary markdown, so this
 * never hands parser output straight to dangerouslySetInnerHTML — DOMPurify
 * strips scripts/styles/event-handler attributes first (MADS-UX §3.3
 * Conversation tab / HANDOFF.md §7).
 */
export function Markdown({ text, className }: { text: string; className?: string }) {
  const html = useMemo(() => {
    const parsed = marked.parse(text, { async: false }) as string;
    return DOMPurify.sanitize(parsed, {
      ALLOWED_TAGS,
      ALLOWED_ATTR: ["href", "title"],
    });
  }, [text]);

  return <div className={className} dangerouslySetInnerHTML={{ __html: html }} />;
}
