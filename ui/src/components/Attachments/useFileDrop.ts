import { useRef, useState, type ClipboardEvent, type DragEvent } from "react";

function carriesFiles(e: DragEvent): boolean {
  return Array.from(e.dataTransfer?.types ?? []).includes("Files");
}

/**
 * Drag-and-drop of files onto a composer, plus paste: both hand the files
 * to `onFiles`. `dragging` drives the drop-target highlight. Only file drags
 * are claimed — dragging text around inside the textarea behaves as usual.
 */
export function useFileDrop(onFiles: (files: File[]) => void) {
  const [dragging, setDragging] = useState(false);
  // dragenter/dragleave fire for every child the pointer crosses; a depth
  // count keeps the highlight steady until it truly leaves the target.
  const depth = useRef(0);

  return {
    dragging,
    dropHandlers: {
      onDragEnter(e: DragEvent) {
        if (!carriesFiles(e)) return;
        e.preventDefault();
        depth.current += 1;
        setDragging(true);
      },
      onDragOver(e: DragEvent) {
        if (!carriesFiles(e)) return;
        // Without this the drop event never fires (and the window navigates to the file).
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
      },
      onDragLeave(e: DragEvent) {
        if (!carriesFiles(e)) return;
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setDragging(false);
      },
      onDrop(e: DragEvent) {
        if (!carriesFiles(e)) return;
        e.preventDefault();
        depth.current = 0;
        setDragging(false);
        onFiles(Array.from(e.dataTransfer.files));
      },
    },
    /**
     * A paste that carries files (a screenshot, files copied in Finder)
     * stages them instead of pasting — Finder would otherwise also paste
     * the file's name as text. A plain-text paste is left alone.
     */
    onPaste(e: ClipboardEvent) {
      const files = Array.from(e.clipboardData?.files ?? []);
      if (files.length === 0) return;
      e.preventDefault();
      onFiles(files);
    },
  };
}
