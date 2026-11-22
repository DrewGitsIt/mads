import type { RouterClient } from "../../../router/client";
import { useGitUpdating } from "../../../router/gitUpdating";
import { useSessionRepos } from "../../../router/repoStateStore";
import { useChangeSet } from "../../../files/useChangeSet";
import styles from "./SessionWorkspace.module.css";

/**
 * The workspace's "updating git state" mark (docs/GIT-STATE-DESIGN.md): a
 * small spinner just after the Changes tab, in the tab bar, so it is in
 * view whichever tab is open. Shown while the Router recomputes any of the
 * session's git-derived parts (router/gitUpdating says which); nothing
 * otherwise.
 *
 * It reads the session's repos and change set itself, from the shared
 * stores (one request each for the whole window, cached on the Router):
 * their subscriptions are what feed the marks, and the tab showing them
 * may not be mounted yet.
 */
export function GitUpdatingIndicator({ routerClient, sessionId }: { routerClient: RouterClient; sessionId: string }) {
  useSessionRepos(routerClient, sessionId);
  useChangeSet(routerClient, sessionId);
  const parts = useGitUpdating(sessionId);
  if (!parts) return null;
  return (
    <span
      className={styles.gitUpdating}
      role="img"
      aria-label="Updating git state"
      title="Updating git state"
      data-testid="git-updating"
      data-what={parts}
    >
      <span className={styles.spin} />
    </span>
  );
}
