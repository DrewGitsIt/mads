export interface AttachedFile {
  id: string;
  name: string;
  size: number;
  type: string;
  /** Object URL for an image thumbnail — undefined for non-image files (they show a glyph instead). */
  previewUrl?: string;
}

export function toAttachedFile(file: File): AttachedFile {
  return {
    id: `${file.name}-${file.size}-${file.lastModified}`,
    name: file.name,
    size: file.size,
    type: file.type,
    previewUrl: file.type.startsWith("image/") ? URL.createObjectURL(file) : undefined,
  };
}

export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unitIndex]}`;
}

/**
 * Injects the "attach a file" boilerplate the user never types — mirrors the
 * Composer's resolve-at-send-time model (mentions are staged as chips, spliced
 * into the outgoing text only at send). Session naming (deriveSessionName)
 * still runs against the raw typed goal, not this preamble.
 */
export function buildGoalText(goalText: string, files: AttachedFile[]): string {
  if (files.length === 0) return goalText;
  const preamble = `See these files for reference: ${files.map((f) => f.name).join(", ")}`;
  return `${preamble}\n\n${goalText}`;
}
