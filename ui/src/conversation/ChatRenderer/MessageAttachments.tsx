import { useState } from "react";
import type { AttachmentRef } from "../attachmentRefs";
import styles from "./MessageAttachments.module.css";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp)$/i;

export interface MessageAttachmentsProps {
  attachments: AttachmentRef[];
  /** Where the Router serves an attachment (or its thumbnail); absent → names only, no links. */
  attachmentUrl?: (attachmentId: string, variant?: "thumbnail") => string;
}

/**
 * The files a user message carried, shown in place of the path list the
 * Router appended for the agent (see attachmentRefs.ts): a thumbnail for an
 * image, a named tile for anything else, each opening the original.
 */
export function MessageAttachments({ attachments, attachmentUrl }: MessageAttachmentsProps) {
  return (
    <div className={styles.attachments}>
      {attachments.map((a) => (
        <AttachmentTile key={a.id} attachment={a} attachmentUrl={attachmentUrl} />
      ))}
    </div>
  );
}

function AttachmentTile({
  attachment,
  attachmentUrl,
}: {
  attachment: AttachmentRef;
  attachmentUrl?: MessageAttachmentsProps["attachmentUrl"];
}) {
  // An image the Router couldn't thumbnail (too large, or not really an
  // image) falls back to the named tile.
  const [thumbFailed, setThumbFailed] = useState(false);
  const showThumb = attachmentUrl && IMAGE_EXT.test(attachment.name) && !thumbFailed;
  const content = showThumb ? (
    <img
      className={styles.thumb}
      src={attachmentUrl(attachment.id, "thumbnail")}
      alt={attachment.name}
      loading="lazy"
      onError={() => setThumbFailed(true)}
    />
  ) : (
    <span className={styles.file}>
      <span aria-hidden="true">📄</span>
      <span className={styles.name}>{attachment.name}</span>
    </span>
  );
  if (!attachmentUrl) {
    return (
      <span className={styles.tile} title={attachment.path}>
        {content}
      </span>
    );
  }
  return (
    <a
      className={styles.tile}
      href={attachmentUrl(attachment.id)}
      target="_blank"
      rel="noreferrer"
      title={attachment.path}
    >
      {content}
    </a>
  );
}
