import { useState, type ReactNode } from "react";
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
import { Banner } from "../components/Banner/Banner";
import { ResizablePanel } from "../components/ResizablePanel/ResizablePanel";
import { IconRail, type IconRailItem } from "../components/IconRail/IconRail";
import { Draft, DraftChip } from "../components/Draft/Draft";
import { Popover } from "../components/Popover/Popover";
import { ContentColumn } from "../components/ContentColumn/ContentColumn";
import { palettes, type PaletteName, type ThemeName } from "../tokens/tokens";
import styles from "./Gallery.module.css";

const PALETTE_NAMES = Object.keys(palettes) as PaletteName[];
const THEME_NAMES: ThemeName[] = ["dark", "light"];

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

function ComboSection({
  palette,
  theme,
  dimmed,
}: {
  palette: PaletteName;
  theme: ThemeName;
  dimmed: boolean;
}) {
  const [chipPressed, setChipPressed] = useState(false);
  const [dismissedToast, setDismissedToast] = useState(false);
  const [popoverOpen, setPopoverOpen] = useState(false);

  return (
    <section
      data-palette={palette}
      data-theme={theme}
      data-testid={`combo-${palette}-${theme}`}
      className={[styles.themeSection, dimmed ? styles.dimmed : ""]
        .filter(Boolean)
        .join(" ")}
    >
      <h2 className={styles.themeTitle}>
        {palette} · {theme}
      </h2>

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
        <Chip
          tone={chipPressed ? "info" : "neutral"}
          pressed={chipPressed}
          onClick={() => setChipPressed((p) => !p)}
        >
          {chipPressed ? "pressed" : "toggle me"}
        </Chip>
      </ComponentGroup>

      <ComponentGroup name="Card">
        <Card>
          <strong>Card title</strong>
          <p>Cards are a bare surface for grouped content.</p>
        </Card>
        <Card hoverable>
          <strong>Hoverable card</strong>
          <p>Hover or tab to it to see the state.</p>
        </Card>
        <Card selected>
          <strong>Selected card</strong>
          <p>Accent border + soft fill.</p>
        </Card>
        <Card>
          <strong>Edit in place</strong>
          <input className={styles.cardInput} defaultValue="Focus me" />
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
        {!dismissedToast && (
          <Toast
            tone="neutral"
            title="neutral toast"
            description="Clears itself in 12s; hover to pause."
            onDismiss={() => setDismissedToast(true)}
          />
        )}
        {TOAST_TONES.filter((tone) => tone !== "neutral").map((tone) => (
          <Toast
            key={tone}
            tone={tone}
            title={`${tone} toast`}
            description="No onDismiss: no ✕, no timer."
            target={tone === "info" ? { label: "Open task", onClick: () => {} } : undefined}
          />
        ))}
      </ComponentGroup>

      <ComponentGroup name="Banner">
        <Banner active tone="warn" title="Live updates paused" description="Reconnecting automatically." action={{ label: "Reload", onClick: () => {} }} />
        <Banner active tone="bad" variant="inline" title="Couldn't load agents" description="Router returned 502." />
        <Banner active tone="bad" variant="compact" title="Couldn't refresh sessions" description="Retrying automatically." action={{ label: "Retry now", onClick: () => {} }} />
      </ComponentGroup>

      <ComponentGroup name="ResizablePanel">
        <ResizablePanel direction="vertical" defaultSize={160} minSize={80} maxSize={320}>
          Drag the top edge to resize.
        </ResizablePanel>
      </ComponentGroup>

      <ComponentGroup name="IconRail">
        <div className={styles.railDemo}>
          <IconRail items={RAIL_ITEMS} activeId="sessions" />
        </div>
      </ComponentGroup>

      <ComponentGroup name="Popover">
        <Popover
          open={popoverOpen}
          onClose={() => setPopoverOpen(false)}
          anchor={
            <Button variant="secondary" size="sm" onClick={() => setPopoverOpen((o) => !o)}>
              Toggle popover
            </Button>
          }
        >
          Popover content — closes on outside click or Escape.
        </Popover>
      </ComponentGroup>

      <ComponentGroup name="Draft">
        <DraftChip />
        <Draft>Synthesized suggestion pending review.</Draft>
      </ComponentGroup>

      <ComponentGroup name="ContentColumn">
        <div className={styles.confined}>
          <ContentColumn variant="narrow">
            <div style={{ background: "var(--bg2)", padding: "8px" }}>narrow (44rem)</div>
          </ContentColumn>
        </div>
      </ComponentGroup>
    </section>
  );
}

export function Gallery() {
  const [filterPalette, setFilterPalette] = useState<PaletteName | null>(null);
  const [filterTheme, setFilterTheme] = useState<ThemeName | null>(null);

  const cycleTheme = () => {
    setFilterTheme((current) =>
      current === null ? "dark" : current === "dark" ? "light" : null,
    );
  };

  return (
    <div className={styles.page}>
      <div className={styles.switcher}>
        <div role="tablist" aria-label="Palette" className={styles.tablist}>
          {PALETTE_NAMES.map((palette) => (
            <button
              key={palette}
              type="button"
              role="tab"
              aria-selected={filterPalette === palette}
              className={styles.tab}
              onClick={() =>
                setFilterPalette((current) => (current === palette ? null : palette))
              }
            >
              {palette}
            </button>
          ))}
        </div>
        <button type="button" className={styles.tab} onClick={cycleTheme}>
          Theme: {filterTheme ?? "all"}
        </button>
      </div>

      {PALETTE_NAMES.map((palette) =>
        THEME_NAMES.map((theme) => {
          const dimmed =
            (filterPalette !== null && filterPalette !== palette) ||
            (filterTheme !== null && filterTheme !== theme);
          return (
            <ComboSection
              key={`${palette}-${theme}`}
              palette={palette}
              theme={theme}
              dimmed={dimmed}
            />
          );
        }),
      )}
    </div>
  );
}
