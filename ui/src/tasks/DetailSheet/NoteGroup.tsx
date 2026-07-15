import { useState } from "react";
import type { Note, NoteType } from "../../router/types";
import { Draft, DraftChip } from "../../components/Draft/Draft";
import { Button } from "../../components/Button/Button";
import styles from "./NoteGroup.module.css";

export interface NoteGroupProps {
  type: NoteType;
  label: string;
  notes: Note[];
  onConfirm: (noteId: number) => void;
  onDiscard: (noteId: number) => void;
}

export function NoteGroup({ type, label, notes, onConfirm, onDiscard }: NoteGroupProps) {
  const [open, setOpen] = useState(true);
  if (notes.length === 0) return null;

  return (
    <section className={styles.group} data-note-type={type}>
      <button type="button" className={styles.heading} onClick={() => setOpen((o) => !o)}>
        {open ? "▾" : "▸"} {label} ({notes.length})
      </button>
      {open && (
        <div className={styles.notes}>
          {notes.map((note) =>
            note.status === "draft" ? (
              <Draft key={note.id} className={styles.note}>
                <div className={styles.noteHeader}>
                  <DraftChip />
                </div>
                <p className={styles.content}>{note.content}</p>
                <div className={styles.actions}>
                  <Button size="sm" onClick={() => onConfirm(note.id!)}>
                    Confirm
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => onDiscard(note.id!)}>
                    Discard
                  </Button>
                </div>
              </Draft>
            ) : (
              <div key={note.id} className={styles.note}>
                <p className={styles.content}>{note.content}</p>
              </div>
            ),
          )}
        </div>
      )}
    </section>
  );
}
