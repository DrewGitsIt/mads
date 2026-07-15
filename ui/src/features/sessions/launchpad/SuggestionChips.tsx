import { Chip } from "../../../components/Chip/Chip";
import { useSuggestions } from "./useSuggestions";
import styles from "./SuggestionChips.module.css";

export interface SuggestionChipsProps {
  onPick: (text: string) => void;
}

export function SuggestionChips({ onPick }: SuggestionChipsProps) {
  const suggestions = useSuggestions();
  if (suggestions.length === 0) return null;

  return (
    <div className={styles.suggWrap}>
      <span className={styles.suggLabel}>From your open work</span>
      <div className={styles.suggRow}>
        {suggestions.map((s) => (
          <Chip key={s.id} onClick={() => onPick(s.text)}>
            {s.text}
          </Chip>
        ))}
      </div>
    </div>
  );
}
