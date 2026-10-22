import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import type { RouterClient } from "../../router/client";
import type { SessionAuth } from "../../router/types";
import type { ShellHostBridge } from "../TerminalRenderer/ptyBridge";
import { TerminalView } from "../TerminalRenderer/TerminalView";
import { commandLine, RunBridge } from "./runBridge";
import styles from "./SignInDialog.module.css";

export interface SignInDialogProps {
  /** The signed-out login; its `login` is the command to run. */
  auth: SessionAuth & { login: NonNullable<SessionAuth["login"]> };
  /** The provider's display name. */
  provider: string;
  routerClient: RouterClient;
  /** window.mads.shell; absent outside Electron, where the dialog shows the command instead. */
  shellHost: ShellHostBridge | undefined;
  /** The login exited 0 and mads was told: close and say so. */
  onSignedIn: () => void;
  onClose: () => void;
}

/**
 * Signing back in (docs/AUTH-SIGNIN-DESIGN.md): the provider's own login
 * command in a small terminal. The CLI opens the browser itself; a flow that
 * asks for a pasted code works because it is a real pty. Exit 0 tells the
 * Router the login is signed in, which clears every session on it. Any other
 * exit keeps the dialog open to try again.
 *
 * Without the shell host (a browser preview) there is nothing to run the
 * command in, so the dialog shows it to copy. A successful turn after a
 * Retry clears the signed-out state on its own.
 *
 * A backdrop click doesn't close it: that would kill a login mid-browser.
 */
export function SignInDialog({ auth, provider, routerClient, shellHost, onSignedIn, onClose }: SignInDialogProps) {
  // Each try is a fresh TerminalView (and so a fresh command); the old one's
  // unmount ends whatever it was running.
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<"running" | "failed" | "finishing">("running");
  const { argv, env } = auth.login;
  // Fixed for the dialog's life: a new bridge would remount the terminal and
  // restart the login under the user (an auth refetch hands a new `auth`).
  const [bridge] = useState(() => (shellHost ? new RunBridge(shellHost, argv, env) : undefined));

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  function handleExit(exitCode: number) {
    if (exitCode !== 0) {
      setState("failed");
      return;
    }
    setState("finishing");
    // A failed POST is not worth stopping for: the login itself worked, and
    // the next successful turn clears the signed-out state anyway.
    void routerClient
      .markSignedIn(auth.account)
      .catch(() => undefined)
      .then(onSignedIn);
  }

  function tryAgain() {
    setState("running");
    setAttempt((n) => n + 1);
  }

  return (
    <div className={styles.backdrop}>
      <div role="dialog" aria-modal="true" aria-labelledby="sign-in-title" className={styles.dialog}>
        <h2 id="sign-in-title" className={styles.title}>
          Sign in to {provider}
        </h2>
        {bridge ? (
          <>
            <p className={styles.description}>
              Finish in the browser window it opens. If it asks for a code, paste it here.
            </p>
            <div className={styles.terminal}>
              <TerminalView key={attempt} bridge={bridge} onExit={handleExit} />
            </div>
            {state === "failed" && (
              <p className={styles.failed} role="status">
                Sign-in didn't finish
              </p>
            )}
          </>
        ) : (
          <>
            <p className={styles.description}>Run this in a terminal, then retry your message.</p>
            <CopyCommand command={commandLine(argv, env)} />
          </>
        )}
        <div className={styles.actions}>
          <Button variant="ghost" onClick={onClose}>
            {bridge ? "Cancel" : "Close"}
          </Button>
          {bridge && state === "failed" && (
            <Button variant="primary" onClick={tryAgain}>
              Try again
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

/** The login command on one line with a Copy button. */
function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);

  function copyCommand() {
    void navigator.clipboard?.writeText(command).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <div className={styles.command}>
      <code>{command}</code>
      <button type="button" className={styles.copy} onClick={copyCommand}>
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
