// The Changes demo page (npm run files → /files.html): the Changes tab and
// the Context Rail's Changes list side by side, against a fake router
// serving the made-up session from docs/mockups/changes-v2.html. A stand-in
// sessions sidebar on the left shows Full-width; ?norail drops the Context
// Rail column, matching the real Changes tab (the rail lives in Conversation).
import { useMemo, useState } from "react";
import { FullWidthContext, type FullWidthApi } from "../features/sessions/fullWidth";
import { RailChangesList } from "../context-rail/RailChangesList";
import { FakeRouterClient } from "../router/fixtures";
import type { FileDiff } from "../router/types";
import { ChangesTab } from "./ChangesTab";
import type { ChangesFocus } from "./changesModel";
import { demoChangeSet, demoDiff, demoSeen } from "./demoChangeSet";
import { saveSeen, useSeenState } from "./seenState";
import { useChangeSet } from "./useChangeSet";
import styles from "./FilesDemo.module.css";

const SESSION = "demo-oauth";

class DemoRouterClient extends FakeRouterClient {
  async getFileDiff(_sessionId: string, path: string): Promise<FileDiff> {
    return demoDiff(path, "", cs.files.find((f) => f.path === path)?.change);
  }
  async getCommitFileDiff(_sessionId: string, hash: string, path: string): Promise<FileDiff> {
    return demoDiff(path, ` @ ${hash.slice(0, 7)}`);
  }
}

const client = new DemoRouterClient();
// ?long pads the session to 70 turns (the strip caps at half its row and scrolls).
const params = new URLSearchParams(location.search);
const cs = demoChangeSet(params.has("long") ? 56 : 0);
const showRail = !params.has("norail");
client.changeSets.set(SESSION, cs);
// Seed "already looked at" once, so the new-since-you-looked dots vary.
if (!localStorage.getItem(`mads.changes.seen.${SESSION}`)) saveSeen(SESSION, demoSeen(cs));

export function FilesDemo() {
  const [focus, setFocus] = useState<ChangesFocus>();
  const rail = useChangeSet(client, SESSION);
  const { isUnseen } = useSeenState(SESSION);
  const [full, setFull] = useState(false);
  const fullWidth = useMemo<FullWidthApi>(() => ({ full, setFull }), [full]);

  return (
    <FullWidthContext.Provider value={fullWidth}>
      <div className={styles.page} data-full-width={full || undefined}>
        <aside className={styles.sessions} aria-label="Sessions (stand-in)">
          <div className={styles.sessionCard}>Review Files at Scale</div>
          <div className={styles.sessionCard}>MADS chat UI bugs</div>
          <div className={styles.sessionCard}>Session Rail Mockup</div>
        </aside>
        <div className={styles.demo}>
          <div className={styles.tabs}>
            <span>Conversation</span>
            <span>Tasks</span>
            <span className={styles.on}>Changes</span>
            <span>Pipeline</span>
          </div>
          <div className={styles.row}>
            <div className={styles.host}>
              <ChangesTab sessionId={SESSION} routerClient={client} focusView={focus} />
            </div>
            {showRail && (
              <aside className={styles.rail} aria-label="Context rail">
                <div className={styles.railHead}>
                  <span className={styles.railTitle}>Changes</span>
                  <span className={styles.railCount}>
                    {rail.changeSet?.files.filter((f) => f.tier !== "scratch").length ?? 0} files
                  </span>
                </div>
                <RailChangesList
                  changeSet={rail.changeSet}
                  loading={rail.loading}
                  error={rail.error}
                  isUnseen={isUnseen}
                  onOpen={(f) => setFocus({ ...f })}
                />
              </aside>
            )}
          </div>
        </div>
      </div>
    </FullWidthContext.Provider>
  );
}
