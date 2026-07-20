import { Chip } from "../../../components/Chip/Chip";
import { useProviderMatrix } from "../state/useProviderMatrix";
import type { ProviderSupport } from "../../../router/types";
import styles from "./ProvidersSection.module.css";

const TONE: Record<ProviderSupport, "ok" | "warn" | "bad"> = {
  native: "ok",
  emulated: "warn",
  unsupported: "bad",
};

/** Provider capability matrix table — rows are behaviors, columns are providers (§11). */
export function ProvidersSection() {
  const { providers, behaviors, loading } = useProviderMatrix();

  if (loading) return <div className={styles.loading}>Loading providers…</div>;

  return (
    <table className={styles.table}>
      <thead>
        <tr>
          <th scope="col">Behavior</th>
          {providers.map((provider) => (
            <th key={provider.name} scope="col">
              {provider.name}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {behaviors.map((behavior) => (
          <tr key={behavior.behavior}>
            <th scope="row">
              {behavior.behavior}
              <span className={styles.level}>{behavior.level}</span>
            </th>
            {providers.map((provider) => {
              const support = provider.matrix[behavior.behavior];
              return (
                <td key={provider.name}>
                  {support && <Chip tone={TONE[support]}>{support}</Chip>}
                </td>
              );
            })}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
