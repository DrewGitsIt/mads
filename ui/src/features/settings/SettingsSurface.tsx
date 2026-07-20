import { useEffect, useRef, useState } from "react";
import { ContentColumn } from "../../components/ContentColumn/ContentColumn";
import { Toast } from "../../components/Toast/Toast";
import { ScrollSpyNav } from "./nav/ScrollSpyNav";
import { useScrollSpy } from "./nav/useScrollSpy";
import { SECTION_IDS, SECTION_LABELS, type SectionId } from "./sections";
import { useConfig } from "./state/useConfig";
import { useProfiles } from "./state/useProfiles";
import { useCredentials } from "./state/useCredentials";
import { useDrift } from "./state/useDrift";
import { DriftBar } from "./DriftBar";
import { ProfilesSection } from "./sections/ProfilesSection";
import { GeneralSection } from "./sections/GeneralSection";
import { PipelineSection } from "./sections/PipelineSection";
import { AppearanceSection } from "./sections/AppearanceSection";
import { CredentialsSection } from "./sections/CredentialsSection";
import { AdvancedSection } from "./sections/AdvancedSection";
import { ProvidersSection } from "./sections/ProvidersSection";
import { UpdatesAboutSection } from "./sections/UpdatesAboutSection";
import type { Config } from "../../router/types";
import styles from "./SettingsSurface.module.css";

function isSectionId(value: string): value is SectionId {
  return (SECTION_IDS as string[]).includes(value);
}

interface ToastState {
  title: string;
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
  const activeId = useScrollSpy(containerRef, sectionRefs);

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
      <div className={styles.body} ref={containerRef}>
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

      {toast && (
        <div className={styles.toastHost}>
          <Toast title={toast.title} description={toast.description} tone="ok" action={toast.action} onDismiss={() => setToast(null)} />
        </div>
      )}
    </div>
  );
}
