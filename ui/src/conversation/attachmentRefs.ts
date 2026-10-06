import type { Attachment } from "../router/types";

// Mirrors services/router/internal/attachments/message.go: the Router
// appends this header and one "- <path>" line per attached file to the
// message the agent gets. Change both together.
export const ATTACHMENTS_HEADER = "Attached files (open each one with your file-reading tool):";

export interface AttachmentRef {
  /** The attachment's id — its path's parent directory (<owner>/<id>/<name>). */
  id: string;
  name: string;
  path: string;
}

/** The same text the Router builds, for the user bubble shown before the transcript catches up. */
export function appendAttachments(text: string, attachments: Attachment[]): string {
  if (attachments.length === 0) return text;
  const body = text.replace(/[ \t\n]+$/, "");
  const list = [ATTACHMENTS_HEADER, ...attachments.map((a) => `- ${a.path}`)].join("\n");
  return body ? `${body}\n\n${list}` : list;
}

/**
 * Splits a user message into what the user typed and the files the Router
 * appended, so the bubble can show thumbnails instead of raw paths. Text
 * that merely mentions the header (anything but a trailing list of paths)
 * is left whole.
 */
export function splitAttachments(text: string): {
  body: string;
  attachments: AttachmentRef[];
} {
  let start: number;
  let body: string;
  if (text.startsWith(`${ATTACHMENTS_HEADER}\n`)) {
    start = 0;
    body = "";
  } else {
    const at = text.lastIndexOf(`\n\n${ATTACHMENTS_HEADER}\n`);
    if (at === -1) return { body: text, attachments: [] };
    start = at + 2;
    body = text.slice(0, at);
  }
  const lines = text.slice(start + ATTACHMENTS_HEADER.length + 1).split("\n");
  const attachments: AttachmentRef[] = [];
  for (const line of lines) {
    const m = /^- (.+)$/.exec(line);
    if (!m) return { body: text, attachments: [] };
    const path = m[1];
    const parts = path.split(/[\\/]/);
    if (parts.length < 3) return { body: text, attachments: [] };
    attachments.push({
      id: parts[parts.length - 2],
      name: parts[parts.length - 1],
      path,
    });
  }
  return { body, attachments };
}
