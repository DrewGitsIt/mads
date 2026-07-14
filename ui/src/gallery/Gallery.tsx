import type { ReactNode } from "react";
import { Button } from "../components/Button/Button";
import { Chip, type ChipTone } from "../components/Chip/Chip";
import { Card } from "../components/Card/Card";
import { Panel, PanelHeader, PanelBody, PanelFooter } from "../components/Panel/Panel";
import {
  SideSheet,
  SideSheetHeader,
  SideSheetBody,
  SideSheetFooter,
} from "../components/SideSheet/SideSheet";
import { Toast, type ToastTone } from "../components/Toast/Toast";
import { ResizablePanel } from "../components/ResizablePanel/ResizablePanel";
import { IconRail, type IconRailItem } from "../components/IconRail/IconRail";
import { Draft, DraftChip } from "../components/Draft/Draft";
import type { ThemeName } from "../tokens/tokens";
import styles from "./Gallery.module.css";

const CHIP_TONES: ChipTone[] = ["neutral", "ok", "warn", "bad", "info", "draft"];
const TOAST_TONES: ToastTone[] = ["neutral", "ok", "warn", "bad", "info"];

const RAIL_ITEMS: IconRailItem[] = [
  { id: "sessions", label: "Sessions", icon: "S" },
  { id: "retro", label: "Retro", icon: "R" },
  { id: "library", label: "Library", icon: "L" },
];

function ComponentGroup({
  name,
  children,
}: {
  name: string;
  children: ReactNode;
}) {
  return (
    <div
      className={styles.group}
      data-testid={`group-${name.toLowerCase().replace(/\s+/g, "-")}`}
    >
      <h3 className={styles.groupTitle}>{name}</h3>
      <div className={styles.row}>{children}</div>
    </div>
  );
}

function ThemeSection({ theme }: { theme: ThemeName }) {
  return (
    <section
      data-theme={theme}
      data-testid={`theme-${theme}`}
      className={styles.themeSection}
    >
      <h2 className={styles.themeTitle}>{theme} theme</h2>

      <ComponentGroup name="Button">
        <Button variant="primary">Primary</Button>
        <Button variant="secondary">Secondary</Button>
        <Button variant="ghost">Ghost</Button>
        <Button variant="primary" size="sm">
          Primary sm
        </Button>
        <Button variant="secondary" disabled>
          Disabled
        </Button>
      </ComponentGroup>

      <ComponentGroup name="Chip">
        {CHIP_TONES.map((tone) => (
          <Chip key={tone} tone={tone}>
            {tone}
          </Chip>
        ))}
      </ComponentGroup>

      <ComponentGroup name="Card">
        <Card>
          <strong>Card title</strong>
          <p>Cards are a bare surface for grouped content.</p>
        </Card>
      </ComponentGroup>

      <ComponentGroup name="Panel">
        <Panel className={styles.confined}>
          <PanelHeader>Inline panel</PanelHeader>
          <PanelBody>Docked content lives here (Context Rail, drill-in).</PanelBody>
          <PanelFooter>
            <Button variant="ghost" size="sm">
              Action
            </Button>
          </PanelFooter>
        </Panel>
      </ComponentGroup>

      <ComponentGroup name="SideSheet">
        <div className={styles.confined}>
          <SideSheet width={320}>
            <SideSheetHeader>Task detail</SideSheetHeader>
            <SideSheetBody>Overlay content, no dimmed backdrop.</SideSheetBody>
            <SideSheetFooter>
              <Button variant="primary" size="sm">
                Save
              </Button>
            </SideSheetFooter>
          </SideSheet>
        </div>
      </ComponentGroup>

      <ComponentGroup name="Toast">
        {TOAST_TONES.map((tone) => (
          <Toast
            key={tone}
            tone={tone}
            title={`${tone} toast`}
            description="Presentational only — no queue/provider yet."
          />
        ))}
      </ComponentGroup>

      <ComponentGroup name="ResizablePanel">
        <ResizablePanel direction="vertical" defaultSize={160} minSize={80} maxSize={320}>
          Drag the top edge to resize.
        </ResizablePanel>
      </ComponentGroup>

      <ComponentGroup name="IconRail">
        <div className={styles.railDemo}>
          <IconRail items={RAIL_ITEMS} activeId="sessions" defaultCollapsed />
        </div>
      </ComponentGroup>

      <ComponentGroup name="Draft">
        <DraftChip />
        <Draft>Synthesized suggestion pending review.</Draft>
      </ComponentGroup>
    </section>
  );
}

export function Gallery() {
  return (
    <div className={styles.page}>
      <ThemeSection theme="dark" />
      <ThemeSection theme="light" />
    </div>
  );
}
