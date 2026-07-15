import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { SideSheet, SideSheetBody, SideSheetFooter, SideSheetHeader } from "../../components/SideSheet/SideSheet";
import { IMPORT_FIXTURES } from "./importFixtures";
import styles from "./ImportFromProviderDialog.module.css";

export interface ImportFromProviderDialogProps {
  onClose: () => void;
}

/** Fixture-only mapping preview — no live provider-import backend exists yet (HANDOFF §5). */
export function ImportFromProviderDialog({ onClose }: ImportFromProviderDialogProps) {
  const [selected, setSelected] = useState(0);
  const fixture = IMPORT_FIXTURES[selected];

  return (
    <SideSheet>
      <SideSheetHeader>Import from provider</SideSheetHeader>
      <SideSheetBody>
        <select
          value={selected}
          onChange={(e) => setSelected(Number(e.target.value))}
          className={styles.select}
        >
          {IMPORT_FIXTURES.map((f, i) => (
            <option key={f.providerName} value={i}>
              {f.providerName}
            </option>
          ))}
        </select>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Provider field</th>
              <th>Agent field</th>
              <th>Value</th>
            </tr>
          </thead>
          <tbody>
            {fixture.fields.map((f) => (
              <tr key={f.providerField}>
                <td>{f.providerField}</td>
                <td>{f.agentField}</td>
                <td>{f.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className={styles.disclaimer}>
          Preview only — live provider import isn't built yet.
        </p>
      </SideSheetBody>
      <SideSheetFooter>
        <Button onClick={onClose}>Close</Button>
      </SideSheetFooter>
    </SideSheet>
  );
}
