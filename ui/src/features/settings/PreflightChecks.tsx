import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Popover } from "../../components/Popover/Popover";
import type { PreflightCheck } from "../../router/types";
import styles from "./PreflightChecks.module.css";

const PROVIDER_PREFIX = "provider:";

export interface PreflightChecksProps {
  checks: PreflightCheck[];
  loading: boolean;
  onRerun: () => void;
}

/**
 * The preflight check list shared by Settings > General and the wizard's
 * PreflightStep. Provider harnesses are optional, so a provider whose binary
 * wasn't found (`not_installed`) is hidden from the health list rather than
 * shown as a failure; the (i) disclosure names every provider preflight
 * probed, detected or not. When none was detected, the list is followed by
 * each supported provider's own install page and documented installer, in
 * alphabetical order so no one harness reads as the recommendation.
 * Optional tools (mergiraf) get a fuller row: what they're for, what was
 * found, or how to install them — a warning when absent, never a failure.
 */
export function PreflightChecks({ checks, loading, onRerun }: PreflightChecksProps) {
  const [infoOpen, setInfoOpen] = useState(false);
  const shown = checks.filter((c) => !c.not_installed);
  const probed = checks.filter((c) => c.name.startsWith(PROVIDER_PREFIX));
  const noneDetected = !loading && probed.length > 0 && probed.every((c) => c.not_installed);

  return (
    <div className={styles.root}>
      <div className={styles.row}>
        <ul className={styles.checks}>
          {shown.map((check) =>
            check.optional ? (
              <OptionalTool key={check.name} check={check} />
            ) : (
              <li key={check.name} className={styles.check}>
                <StatusIcon check={check} />
                {check.name}
                {!check.healthy && check.fix_hint && <span className={styles.hint}>{check.fix_hint}</span>}
              </li>
            ),
          )}
        </ul>
        {probed.length > 0 && (
          <Popover
            open={infoOpen}
            onClose={() => setInfoOpen(false)}
            className={styles.info}
            anchor={
              <button
                type="button"
                className={styles.infoButton}
                aria-label="Providers checked"
                aria-expanded={infoOpen}
                onClick={() => setInfoOpen((open) => !open)}
              >
                i
              </button>
            }
          >
            <div className={styles.infoTitle}>Providers checked</div>
            <ul className={styles.probed}>
              {probed.map((check) => (
                <li key={check.name}>
                  <span className={styles.probedName}>{providerName(check)}</span>
                  <span>{check.not_installed ? "not detected" : (check.detail ?? "detected")}</span>
                  {check.not_installed && check.fix_hint && (
                    <span className={styles.probedHint}>{check.fix_hint}</span>
                  )}
                </li>
              ))}
            </ul>
          </Popover>
        )}
      </div>
      {noneDetected && <NoProviderDetected providers={probed} loading={loading} onRerun={onRerun} />}
    </div>
  );
}

/** ✓ healthy, ⚠ a caution (e.g. a provider newer than its proven range), ✗ a failure. */
function StatusIcon({ check }: { check: PreflightCheck }) {
  if (check.healthy) return <span aria-hidden="true">✓</span>;
  if (check.warning) {
    return (
      <span aria-hidden="true" className={styles.warnIcon}>
        ⚠
      </span>
    );
  }
  return <span aria-hidden="true">✗</span>;
}

/**
 * An optional tool's row: name and an "optional" tag, what it's for, then
 * the version and path found — or, when absent, how to install it.
 */
function OptionalTool({ check }: { check: PreflightCheck }) {
  return (
    <li className={styles.optionalTool} data-preflight-check={check.name}>
      <div className={styles.check}>
        <StatusIcon check={check} />
        {check.name}
        <span className={styles.optionalTag}>optional</span>
      </div>
      <div className={styles.toolBody}>
        {check.purpose && <span>{check.purpose}</span>}
        {check.healthy ? (
          <span className={styles.toolFound}>
            {check.version ?? "Found"}
            {check.path ? (
              <>
                {" at "}
                <code>{check.path}</code>
              </>
            ) : null}
          </span>
        ) : (
          <>
            {check.detail && <span className={styles.hint}>{capitalize(check.detail)}.</span>}
            {check.install_command && <CopyCommand command={check.install_command} />}
            {check.install_url && (
              <span>
                Or download the release binary:{" "}
                <a href={check.install_url} target="_blank" rel="noreferrer" className={styles.link}>
                  {check.install_url.replace(/^https:\/\//, "")} ↗
                </a>
              </span>
            )}
          </>
        )}
      </div>
    </li>
  );
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function providerName(check: PreflightCheck): string {
  return check.name.slice(PROVIDER_PREFIX.length);
}

interface NoProviderDetectedProps {
  providers: PreflightCheck[];
  loading: boolean;
  onRerun: () => void;
}

function NoProviderDetected({ providers, loading, onRerun }: NoProviderDetectedProps) {
  const sorted = [...providers].sort((a, b) => providerName(a).localeCompare(providerName(b)));
  return (
    <section className={styles.empty} aria-label="No agent harness detected">
      <div className={styles.emptyTitle}>No agent harness detected</div>
      <p className={styles.emptyBody}>
        mads runs sessions through one of these agents. Install any one of them, then check again.
      </p>
      <ul className={styles.options}>
        {sorted.map((check) => (
          <InstallOption key={check.name} check={check} />
        ))}
      </ul>
      <Button variant="secondary" size="sm" onClick={onRerun} disabled={loading}>
        Check again
      </Button>
    </section>
  );
}

function InstallOption({ check }: { check: PreflightCheck }) {
  const command = check.install_command;
  return (
    <li className={styles.option}>
      <div className={styles.optionHead}>
        <span className={styles.probedName}>{providerName(check)}</span>
        {check.install_url && (
          <a href={check.install_url} target="_blank" rel="noreferrer" className={styles.link}>
            Install guide ↗
          </a>
        )}
      </div>
      {command && <CopyCommand command={command} />}
    </li>
  );
}

/** A one-line install command with a Copy button. */
function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);

  function copyCommand() {
    void navigator.clipboard.writeText(command).then(() => {
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
