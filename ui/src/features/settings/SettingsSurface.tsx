import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog/ConfirmDialog";
import { ContentColumn } from "../../components/ContentColumn/ContentColumn";
import { Toast, type ToastTone } from "../../components/Toast/Toast";
import { registerLeaveGuard } from "../../appNavigation";
import { ScrollSpyNav } from "./nav/ScrollSpyNav";
import { useScrollSpy } from "./nav/useScrollSpy";
import { SECTION_IDS, SECTION_LABELS, type SectionId } from "./sections";
import { useConfig } from "./state/useConfig";
import { useProfiles } from "./state/useProfiles";
import { useCredentials } from "./state/useCredentials";
import { useDrift } from "./state/useDrift";
import { DriftBar } from "./DriftBar";
import { SaveBar } from "./SaveBar";
import { restartFieldsIn } from "./dirtyFields";
import { ProfilesSection } from "./sections/ProfilesSection";
import { GeneralSection } from "./sections/GeneralSection";
import { PipelineSection } from "./sections/PipelineSection";
import { AppearanceSection } from "./sections/AppearanceSection";
import { CredentialsSection } from "./sections/CredentialsSection";
import { AdvancedSection } from "./sections/AdvancedSection";
import { ShellsSection } from "./sections/ShellsSection";
import { ProvidersSection } from "./sections/ProvidersSection";
import { UpdatesAboutSection } from "./sections/UpdatesAboutSection";
import type { Config } from "../../router/types";
import styles from "./SettingsSurface.module.css";

function isSectionId(value: string): value is SectionId {
  return (SECTION_IDS as string[]).includes(value);
}

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

interface ToastState {
  title: string;
  tone?: ToastTone;
  description?: string;
  action?: { label: string; onClick: () => void };
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

  const scrolledToHash = useRef(false);
  useEffect(() => {
    if (scrolledToHash.current || config.loading || !config.draft) return;
    scrolledToHash.current = true;
    const hash = window.location.hash.slice(1);
    if (isSectionId(hash)) {
      sectionRefs[hash].current?.scrollIntoView({ block: "start" });
    }
  }, [config.loading, config.draft, sectionRefs]);

  const [toast, setToast] = useState<ToastState | null>(null);

  // One save bar for the whole page (live controls — Appearance, feature
  // flags — persist themselves via saveFields and never show up here).
  const [saving, setSaving] = useState(false);
  const [errorScroll, setErrorScroll] = useState(0);
  const dirty = config.dirtyFields;

  const saveDraft = async () => {
    const needsRestart = restartFieldsIn(dirty).length > 0;
    setSaving(true);
    const ok = await config.save();
    setSaving(false);
    if (ok && needsRestart) setToast({ title: "Saved. Some changes apply when mads restarts." });
    if (!ok) setErrorScroll((n) => n + 1);
    return ok;
  };

  const discardDraft = () => {
    if (config.saved) config.setDraft(config.saved);
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
    setToast({
      title: "Profile switched",
      description: restartRequired ? "Some changes apply on mads restart." : undefined,
      action: {
        label: "Undo",
        onClick: async () => {
          await config.save(previousConfig);
          setToast(null);
        },
      },
    });
  };

  if (config.loading || !config.draft) {
    return <div className={styles.loading}>Loading settings…</div>;
  }

  return (
    <div className={styles.surface}>
      <ScrollSpyNav activeId={activeId} onNavigate={handleNavigate} />
      <div className={styles.main}>
        <div className={dirty.length > 0 ? `${styles.body} ${styles.withSaveBar}` : styles.body} ref={containerRef}>
          <ContentColumn variant="wide">
            <DriftBar drift={drift} />

            <section id="profiles" ref={sectionRefs.profiles} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.profiles}</h2>
              <ProfilesSection
                config={config}
                profiles={profiles}
                credentialNames={credentials.names}
                onSwitched={handleSwitched}
              />
            </section>

            <section id="general" ref={sectionRefs.general} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.general}</h2>
              <GeneralSection config={config} />
            </section>

            <section id="pipeline" ref={sectionRefs.pipeline} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.pipeline}</h2>
              <PipelineSection config={config} />
            </section>

            <section id="appearance" ref={sectionRefs.appearance} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.appearance}</h2>
              <AppearanceSection config={config} />
            </section>

            <section id="credentials" ref={sectionRefs.credentials} className={styles.section}>
              <CredentialsSection config={config} profiles={profiles.profiles} />
            </section>

            <section id="advanced" ref={sectionRefs.advanced} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.advanced}</h2>
              <AdvancedSection config={config} />
            </section>

            <section id="shells" ref={sectionRefs.shells} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.shells}</h2>
              <ShellsSection />
            </section>

            <section id="providers" ref={sectionRefs.providers} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.providers}</h2>
              <ProvidersSection />
            </section>

            <section id="updates" ref={sectionRefs.updates} className={styles.section}>
              <h2 className={styles.heading}>{SECTION_LABELS.updates}</h2>
              <UpdatesAboutSection />
            </section>
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
            description={toast.description}
            tone={toast.tone ?? "ok"}
            action={toast.action}
            onDismiss={() => setToast(null)}
          />
        </div>
      )}
    </div>
  );
}
