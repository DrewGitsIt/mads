import { Chip } from "../../../components/Chip/Chip";
import { useSuggestions, type Suggestion } from "./useSuggestions";
import styles from "./SuggestionChips.module.css";

export interface SuggestionChipsProps {
  /** Navigational, not text-injecting: "Resume" opens/resumes the session, "Pick up" opens the session on its ready task. */
  onPick: (suggestion: Suggestion) => void;
}

export function SuggestionChips({ onPick }: SuggestionChipsProps) {
  const suggestions = useSuggestions();
  if (suggestions.length === 0) return null;

  return (
    <div className={styles.suggWrap}>
      <span className={styles.suggLabel}>From your open work</span>
      <div className={styles.suggRow}>
        {suggestions.map((s) => (
          <Chip key={s.id} onClick={() => onPick(s)}>
            {s.label}
          </Chip>
        ))}
      </div>
    </div>
  );
}
