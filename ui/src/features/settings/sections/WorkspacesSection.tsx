import { requestOpenLibrary } from "../../../appNavigation";
import { SettingsCard } from "../SettingsCard";
import { InlineLink } from "../StatusLine";
import styles from "./WorkspacesSection.module.css";

/**
 * What a workspace is, and the way to the Library where they're made and
 * edited. This section is deliberately short for now; listing workspaces
 * and their defaults here is planned for a later pass.
 */
export function WorkspacesSection() {
  return (
    <div className={styles.panel}>
      <SettingsCard>
        <div className={styles.body}>
          <p className={styles.text}>
            A workspace names the repos a piece of work spans, with one marked primary, plus steering files and
            defaults of its own. A session started in a workspace begins in its primary repo, and the workspace&apos;s
            provider, model, persona, isolation and pipeline template win over the defaults on this page.
          </p>
          {/* Leaving Settings: App routes this through the unsaved-changes guard. */}
          <InlineLink onClick={() => requestOpenLibrary("workspaces")}>Open workspaces in the Library →</InlineLink>
        </div>
      </SettingsCard>
    </div>
  );
}
