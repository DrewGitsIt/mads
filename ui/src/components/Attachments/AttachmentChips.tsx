import { Chip } from "../Chip/Chip";
import { formatFileSize, type StagedAttachment } from "./stagedAttachments";
import styles from "./AttachmentChips.module.css";

export interface AttachmentChipsProps {
  items: StagedAttachment[];
  /** The thumbnail to show for an uploaded image; undefined shows a file glyph. */
  thumbnailUrl: (item: StagedAttachment) => string | undefined;
  onRemove: (key: string) => void;
}

/**
 * A composer's staged files, each removable before send. Same staged-chip
 * strip as Composer/InjectedContentPreview.tsx, with a thumbnail once the
 * Router has the file (it makes thumbnails; the browser never decodes the
 * full-size original just to preview it).
 */
export function AttachmentChips({ items, thumbnailUrl, onRemove }: AttachmentChipsProps) {
  if (items.length === 0) return null;

  return (
    <div className={styles.preview}>
      {items.map((item) => {
        const thumb = item.status === "ready" ? thumbnailUrl(item) : undefined;
        return (
          <Chip
            key={item.key}
            tone={item.status === "failed" ? "bad" : "neutral"}
            className={styles.chip}
          >
            {thumb ? (
              <img className={styles.thumb} src={thumb} alt="" />
            ) : (
              <span className={styles.glyph} aria-hidden="true">
                {item.status === "uploading" ? "⋯" : item.status === "failed" ? "⚠" : "📄"}
              </span>
            )}
            <span className={styles.name} title={item.error ?? item.name}>
              {item.name}
            </span>
            <span className={styles.size}>
              {item.status === "uploading"
                ? "Uploading…"
                : item.status === "failed"
                  ? "Upload failed"
                  : formatFileSize(item.size)}
            </span>
            <button
              type="button"
              className={styles.remove}
              aria-label={`Remove ${item.name}`}
              onClick={() => onRemove(item.key)}
            >
              ✕
            </button>
          </Chip>
        );
      })}
    </div>
  );
}
