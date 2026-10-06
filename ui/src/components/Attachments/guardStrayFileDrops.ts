/**
 * A file dropped anywhere that isn't a drop target (useFileDrop) would make
 * the window navigate to it — in Electron, replacing the whole app with the
 * file. Claiming every file drag at the document stops that; the real drop
 * targets still get theirs first.
 */
export function guardStrayFileDrops(doc: Document = document): void {
  const claim = (e: DragEvent) => {
    if (e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files")) e.preventDefault();
  };
  doc.addEventListener("dragover", claim);
  doc.addEventListener("drop", claim);
}
