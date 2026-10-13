import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import type { ExportFormat } from "../../../router/types";
import styles from "./ExportCard.module.css";

type Format = Exclude<ExportFormat, "">;

const FORMATS: { id: Format; title: string; desc: string }[] = [
  { id: "md", title: "Markdown", desc: "For reading and sharing" },
  { id: "json", title: "JSON", desc: "Complete, machine-readable" },
];

/** The /export card: pick a format, optionally remember it, download. */
export function ExportCard({
  onDownload,
}: {
  onDownload: (format: Format, remember: boolean) => void;
}) {
  const [format, setFormat] = useState<Format>("md");
  const [remember, setRemember] = useState(true);
  return (
    <div className={styles.card}>
      <div className={styles.header}>Export conversation</div>
      <div className={styles.body} role="radiogroup" aria-label="Export format">
        {FORMATS.map((f) => (
          <button
            key={f.id}
            type="button"
            role="radio"
            aria-checked={format === f.id}
            className={styles.fmt}
            onClick={() => setFormat(f.id)}
          >
            <span className={styles.title}>{f.title}</span>
            <span className={styles.desc}>{f.desc}</span>
          </button>
        ))}
      </div>
      <div className={styles.footer}>
        <label className={styles.remember}>
          <input
            type="checkbox"
            checked={remember}
            onChange={(e) => setRemember(e.target.checked)}
          />
          Always use this format
        </label>
        <Button
          variant="primary"
          autoFocus
          onClick={() => onDownload(format, remember)}
        >
          Download
        </Button>
      </div>
    </div>
  );
}
