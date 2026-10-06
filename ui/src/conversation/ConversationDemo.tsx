import { useState } from "react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import type { ShellDockBridge } from "./ShellDock/shellBridge";
import styles from "./ConversationDemo.module.css";

const client = new FakeRouterClient();
const sessionIds = Object.keys(fixtureSessions);

/**
 * Toy in-memory pty: echoes typed input back with a fake prompt so terminal
 * mode and the Shell dock are visually reviewable outside Electron, without
 * pulling in node-pty (HANDOFF.md §8 — demo mount, not a real shell).
 */
function makeDemoPtyBridge(): ShellDockBridge {
  const dataListeners = new Map<string, Set<(data: string) => void>>();
  let nextId = 1;

  return {
    async spawn() {
      const id = `demo-${nextId++}`;
      dataListeners.set(id, new Set());
      setTimeout(() => {
        dataListeners.get(id)?.forEach((cb) => cb("mads demo shell\r\n$ "));
      }, 0);
      return id;
    },
    write(ptyId, data) {
      dataListeners.get(ptyId)?.forEach((cb) => cb(data === "\r" ? "\r\n$ " : data));
    },
    resize() {},
    async inspect() {
      return undefined; // the demo shell is never busy
    },
    kill(ptyId) {
      dataListeners.delete(ptyId);
    },
    onData(ptyId, listener) {
      const set = dataListeners.get(ptyId) ?? new Set();
      set.add(listener);
      dataListeners.set(ptyId, set);
      return () => set.delete(listener);
    },
    onExit() {
      return () => {};
    },
    end() {},
    onDetached() {
      return () => {};
    },
    onHostExited() {
      return () => {};
    },
  };
}

const demoBridge = makeDemoPtyBridge();

export function ConversationDemo() {
  const [sessionId, setSessionId] = useState(sessionIds[0]);

  return (
    <div className={styles.demo}>
      <div className={styles.picker}>
        {sessionIds.map((id) => (
          <button
            key={id}
            type="button"
            className={id === sessionId ? styles.active : ""}
            onClick={() => setSessionId(id)}
          >
            {id}
          </button>
        ))}
      </div>
      <div className={styles.host}>
        <ConversationTab
          key={sessionId}
          sessionId={sessionId}
          routerClient={client}
          shellBridge={demoBridge}
        />
      </div>
    </div>
  );
}
