import { useEffect, useRef, useState, type ReactNode } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog/ConfirmDialog";
import { ContentColumn } from "../../components/ContentColumn/ContentColumn";
import { Toast, type ToastTone } from "../../components/Toast/Toast";
import { registerLeaveGuard } from "../../appNavigation";
import { ScrollSpyNav } from "./nav/ScrollSpyNav";
import { useScrollSpy } from "./nav/useScrollSpy";
import {
  LIVE_SECTIONS,
  SECTION_IDS,
  SECTION_LABELS,
  SECTION_LEDES,
  resolveSectionHash,
  type SectionId,
  type SectionIntent,
} from "./sections";
import { useConfig } from "./state/useConfig";
import { useProfiles } from "./state/useProfiles";
import { useCredentials } from "./state/useCredentials";
import { useDrift } from "./state/useDrift";
import { DriftBar } from "./DriftBar";
import { SaveBar } from "./SaveBar";
import { restartFieldsIn } from "./dirtyFields";
import { SettingTag } from "./SettingRow";
import { ProfilesSection } from "./sections/ProfilesSection";
import { SessionsSection } from "./sections/SessionsSection";
import { ModelsSection } from "./sections/ModelsSection";
import { WorkspacesSection } from "./sections/WorkspacesSection";
import { PipelinesSection } from "./sections/PipelinesSection";
import { SynthesisSection } from "./sections/SynthesisSection";
import { AppearanceSection } from "./sections/AppearanceSection";
import { EditorSection } from "./sections/EditorSection";
import { CredentialsSection } from "./sections/CredentialsSection";
import { ExperimentalSection } from "./sections/ExperimentalSection";
import { SystemSection, SHELLS_ANCHOR_ID } from "./sections/SystemSection";
import { AboutSection } from "./sections/AboutSection";
import type { Config } from "../../router/types";
import styles from "./SettingsSurface.module.css";

/**
 * The first control (in page order) with a field error. Inputs carry their
 * Config field's name kebab-cased as their id (concurrency_limit →
 * #concurrency-limit); a field without one falls back to its row's message.
 */
function firstErrorTarget(root: HTMLElement, fields: string[]): HTMLElement | null {
  const targets = fields
    .map((f) => root.querySelector<HTMLElement>(`#${CSS.escape(f.replaceAll("_", "-"))}`))
    .filter((el): el is HTMLElement => el !== null);
  if (targets.length === 0) return root.querySelector<HTMLElement>("[data-setting-error]");
  return targets.reduce((first, el) =>
    first.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_PRECEDING ? el : first,
  );
}

/**
 * What an aliased link asked for beyond its section. "shells" (the dock's
 * Manage, old #shells links) brings the Shells row into view and opens its
 * inventory sheet.
 */
function revealIntent(intent: SectionIntent, openShells: (open: boolean) => void) {
  if (intent === "shells") {
    document.getElementById(SHELLS_ANCHOR_ID)?.scrollIntoView({ block: "center" });
    openShells(true);
  }
}

interface ToastState {
  title: string;
  tone?: ToastTone;
  description?: string;
  /** The config a profile switch replaced — the toast offers Undo back to it. */
  undoTo?: Config;
}

/** One continuously-scrolling page (HANDOFF §2) — no more tab-routed sub-panels. */
export function SettingsSurface() {
  const config = useConfig();
  const profiles = useProfiles();
  const credentials = useCredentials();
  const drift = useDrift(config, profiles, credentials.names);

  type MutableSectionRefs = Record<SectionId, { current: HTMLElement | null }>;
  const containerRef = useRef<HTMLDivElement>(null);
  const sectionRefs = useRef<MutableSectionRefs>(
    Object.fromEntries(SECTION_IDS.map((id) => [id, { current: null }])) as MutableSectionRefs,
  ).current;
  const activeId = useScrollSpy(containerRef, sectionRefs, !config.loading && !!config.draft);

  // The shells inventory sheet (System → Shells → Manage…), lifted here so a
  // #shells link can open it.
  const [shellsOpen, setShellsOpen] = useState(false);

  const scrolledToHash = useRef(false);
  useEffect(() => {
    if (scrolledToHash.current || config.loading || !config.draft) return;
    scrolledToHash.current = true;
    const target = resolveSectionHash(window.location.hash.slice(1));
    if (!target) return;
    if (target.intent) revealIntent(target.intent, setShellsOpen);
    else sectionRefs[target.section].current?.scrollIntoView({ block: "start" });
    // Old ids land on the section that now holds their content.
    if (window.location.hash.slice(1) !== target.section) history.replaceState(null, "", `#${target.section}`);
  }, [config.loading, config.draft, sectionRefs]);

  const [toast, setToast] = useState<ToastState | null>(null);

  // One save bar for the whole page (live controls — Appearance, feature
  // flags — persist themselves via saveFields and never show up here).
  const [saving, setSaving] = useState(false);
  const [errorScroll, setErrorScroll] = useState(0);
  // Bumped whenever the draft is reset to the saved config, so view state
  // layered on it (Start in's picked-but-empty choice) resets too.
  const [draftEpoch, setDraftEpoch] = useState(0);
  const dirty = config.dirtyFields;

  const saveDraft = async () => {
    const needsRestart = restartFieldsIn(dirty).length > 0;
    setSaving(true);
    const ok = await config.save();
    setSaving(false);
    if (ok) setDraftEpoch((n) => n + 1);
    if (ok && needsRestart) setToast({ title: "Saved. Some changes apply when mads restarts." });
    if (!ok) setErrorScroll((n) => n + 1);
    return ok;
  };

  const discardDraft = () => {
    if (config.saved) config.setDraft(config.saved);
    setDraftEpoch((n) => n + 1);
  };

  // After a failed save renders its field errors, bring the first into view.
  const fieldErrors = config.fieldErrors;
  useEffect(() => {
    if (errorScroll === 0 || !containerRef.current) return;
    const target = firstErrorTarget(containerRef.current, Object.keys(fieldErrors));
    if (target) {
      target.scrollIntoView({ block: "center" });
      target.focus({ preventScroll: true });
    } else {
      setToast({ title: "Couldn't save settings", description: "Check the Router is running and try again.", tone: "bad" });
    }
    // fieldErrors is the same render's — only a new failed save should scroll.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [errorScroll]);

  // Leaving Settings with unsaved edits asks first (App consults the guard
  // before any surface switch). Refs keep the registered guard stable.
  const [pendingLeave, setPendingLeave] = useState<(() => void) | null>(null);
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty.length > 0;
  useEffect(
    () =>
      registerLeaveGuard((proceed) => {
        if (!dirtyRef.current) return false;
        setPendingLeave(() => proceed);
        return true;
      }),
    [],
  );

  const leaveAfterSave = async () => {
    const proceed = pendingLeave;
    setPendingLeave(null);
    if ((await saveDraft()) && proceed) proceed();
  };

  const leaveDiscarding = () => {
    const proceed = pendingLeave;
    setPendingLeave(null);
    discardDraft();
    proceed?.();
  };

  const handleNavigate = (id: SectionId) => {
    sectionRefs[id].current?.scrollIntoView({ block: "start" });
    history.replaceState(null, "", `#${id}`);
  };

  const handleSwitched = (previousConfig: Config, restartRequired: boolean) => {
    setDraftEpoch((n) => n + 1);
    setToast({
      title: "Profile switched",
      description: restartRequired ? "Some changes apply on mads restart." : undefined,
      undoTo: previousConfig,
    });
  };

  // Undo saves the whole pre-switch config, which would overwrite any edit
  // made since — so, like switching itself, it waits while edits are unsaved.
  const undoBlocked = dirty.length > 0;
  const undoSwitch = async (previousConfig: Config) => {
    if (undoBlocked) return;
    if (await config.save(previousConfig)) setDraftEpoch((n) => n + 1);
    setToast(null);
  };

  if (config.loading || !config.draft) {
    return <div className={styles.loading}>Loading settings…</div>;
  }

  const sectionContent: Record<SectionId, ReactNode> = {
    profiles: (
      <ProfilesSection
        config={config}
        profiles={profiles}
        credentialNames={credentials.names}
        onSwitched={handleSwitched}
      />
    ),
    sessions: <SessionsSection config={config} onNavigate={handleNavigate} resetKey={draftEpoch} />,
    models: <ModelsSection config={config} />,
    workspaces: <WorkspacesSection />,
    pipelines: <PipelinesSection config={config} />,
    synthesis: <SynthesisSection config={config} />,
    appearance: <AppearanceSection config={config} />,
    editor: <EditorSection config={config} />,
    credentials: <CredentialsSection config={config} profiles={profiles.profiles} />,
    experimental: <ExperimentalSection config={config} />,
    system: <SystemSection config={config} shellsOpen={shellsOpen} onShellsOpenChange={setShellsOpen} />,
    about: <AboutSection />,
  };

  return (
    <div className={styles.surface}>
      <ScrollSpyNav activeId={activeId} onNavigate={handleNavigate} />
      <div className={styles.main}>
        <div className={styles.body} ref={containerRef}>
          <ContentColumn variant="wide">
            <DriftBar drift={drift} />

            {SECTION_IDS.map((id) => (
              <section key={id} id={id} ref={sectionRefs[id]} className={styles.section}>
                <h2 className={styles.heading}>{SECTION_LABELS[id]}</h2>
                {SECTION_LEDES[id] && (
                  <p className={styles.lede}>
                    {/* Unsaved edits hold back Switch and the new-profile card; say so in
                        the lede's own line rather than adding one, so the page below
                        doesn't shift as the save bar comes and goes. */}
                    {id === "profiles" && dirty.length > 0
                      ? "Save or discard your changes first to switch or create profiles."
                      : SECTION_LEDES[id]}
                    {LIVE_SECTIONS.has(id) && <SettingTag tone="info">Live</SettingTag>}
                  </p>
                )}
                {sectionContent[id]}
              </section>
            ))}
          </ContentColumn>
        </div>
        <SaveBar
          count={dirty.length}
          restartNeeded={restartFieldsIn(dirty).length > 0}
          saving={saving}
          onDiscard={discardDraft}
          onSave={() => void saveDraft()}
        />
      </div>

      <ConfirmDialog
        open={pendingLeave !== null}
        title="Unsaved changes"
        description={`You have unsaved changes to ${dirty.length} setting${dirty.length === 1 ? "" : "s"}. Save them before leaving Settings?`}
        confirmLabel="Save"
        confirmTone="primary"
        confirmDisabled={saving}
        onConfirm={() => void leaveAfterSave()}
        secondaryLabel="Discard"
        onSecondary={leaveDiscarding}
        cancelLabel="Keep editing"
        onCancel={() => setPendingLeave(null)}
      />

      {toast && (
        <div className={styles.toastHost}>
          <Toast
            title={toast.title}
            description={
              toast.undoTo && undoBlocked
                ? [toast.description, "Save or discard your changes to undo."].filter(Boolean).join(" ")
                : toast.description
            }
            tone={toast.tone ?? "ok"}
            action={
              toast.undoTo && {
                label: "Undo",
                disabled: undoBlocked,
                onClick: () => void undoSwitch(toast.undoTo!),
              }
            }
            onDismiss={() => setToast(null)}
          />
        </div>
      )}
    </div>
  );
}
