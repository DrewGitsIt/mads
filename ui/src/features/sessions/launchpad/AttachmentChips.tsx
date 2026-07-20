import { Chip } from "../../../components/Chip/Chip";
import { formatFileSize, type AttachedFile } from "./attachments";
import styles from "./AttachmentChips.module.css";

export interface AttachmentChipsProps {
  files: AttachedFile[];
  onRemove: (id: string) => void;
}

/** Same staged-chip-strip pattern as Composer/InjectedContentPreview.tsx, adapted for thumbnails/size. */
export function AttachmentChips({ files, onRemove }: AttachmentChipsProps) {
  if (files.length === 0) return null;

  return (
    <div className={styles.preview}>
      {files.map((file) => (
        <Chip key={file.id} tone="neutral" className={styles.chip}>
          {file.previewUrl ? (
            <img className={styles.thumb} src={file.previewUrl} alt="" />
          ) : (
            <span className={styles.glyph} aria-hidden="true">
              📄
            </span>
          )}
          <span className={styles.name}>{file.name}</span>
          <span className={styles.size}>{formatFileSize(file.size)}</span>
          <button
            type="button"
            className={styles.remove}
            aria-label={`Remove ${file.name}`}
            onClick={() => onRemove(file.id)}
          >
            ✕
          </button>
        </Chip>
      ))}
    </div>
  );
}
