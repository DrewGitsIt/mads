import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsSurface } from "./SettingsSurface";
import { guardLeave } from "../../appNavigation";
import type { Config } from "../../router/types";
import { FIELD_SECTION, SECTION_IDS, SECTION_LABELS, SECTION_LEDES, type SectionId } from "./sections";
import {
  fixtureConfig,
  fixturePreflightChecks,
  fixtureProfiles,
  fixtureProviderBehaviors,
  fixtureProviders,
  fixtureVersionInfo,
} from "../../router/fixtures";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

// The Router's config, as PUT /config leaves it. `rejectPut` answers the next
// PUT with a validation error instead.
let server: { config: Config; rejectPut?: { field: string; message: string }[]; puts: Config[] };

function stubRouter() {
  server = { config: { ...fixtureConfig }, puts: [] };
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
      const url = input.toString();
      if (init?.method === "PUT" && url.includes("/config")) {
        if (server.rejectPut) {
          const errors = server.rejectPut;
          server.rejectPut = undefined;
          return Promise.resolve(jsonResponse({ errors }, 400));
        }
        const body = JSON.parse(init.body as string) as Config;
        server.puts.push(body);
        server.config = body;
        return Promise.resolve(jsonResponse(body));
      }
      if (url.includes("/credentials/env-candidates"))
        return Promise.resolve(jsonResponse({ candidates: [], looked_for: [] }));
      if (url.includes("/credentials")) return Promise.resolve(jsonResponse([]));
      if (url.includes("/preflight")) return Promise.resolve(jsonResponse(fixturePreflightChecks));
      if (init?.method === "POST" && url.includes("/activate")) {
        server.config = { ...server.config, ...fixtureProfiles[0].snapshot, active_profile_id: fixtureProfiles[0].id };
        return Promise.resolve(jsonResponse(server.config));
      }
      if (url.includes("/profiles")) return Promise.resolve(jsonResponse(fixtureProfiles));
      if (url.includes("/providers/behaviors")) return Promise.resolve(jsonResponse(fixtureProviderBehaviors));
      if (url.includes("/providers")) return Promise.resolve(jsonResponse(fixtureProviders));
      if (url.includes("/workspaces")) return Promise.resolve(jsonResponse([]));
      if (url.includes("/version")) return Promise.resolve(jsonResponse(fixtureVersionInfo));
      return Promise.resolve(jsonResponse(server.config));
    }),
  );
}

describe("SettingsSurface", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.location.hash = "";
  });

  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
  });

  it("renders every former panel's section in one scrollable page", async () => {
    stubRouter();
    render(<SettingsSurface />);

    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    expect(screen.getByText("Concurrency limit")).toBeTruthy();
    expect(screen.getByText(/no credentials stored/i)).toBeTruthy();
    expect(screen.getByText("Provider capabilities")).toBeTruthy();
    await waitFor(() => expect(screen.getByText("Version")).toBeTruthy());
  });

  it("renders the sections in nav order, each with its heading and lede", async () => {
    stubRouter();
    const { container } = render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    const sections = Array.from(container.querySelectorAll("section[id]")).filter((s) =>
      (SECTION_IDS as string[]).includes(s.id),
    );
    expect(sections.map((s) => s.id)).toEqual(SECTION_IDS);
    for (const section of sections) {
      const id = section.id as SectionId;
      expect(within(section as HTMLElement).getAllByRole("heading", { level: 2 })[0].textContent).toBe(
        SECTION_LABELS[id],
      );
      if (SECTION_LEDES[id]) expect(section.textContent).toContain(SECTION_LEDES[id]);
    }
    // Live sections say so beside their lede.
    expect(within(document.getElementById("appearance")!).getByText("Live")).toBeTruthy();
    expect(within(document.getElementById("experimental")!).getByText("Live")).toBeTruthy();
    expect(within(document.getElementById("pipelines")!).queryByText("Live")).toBeNull();
  });

  it("scrolls the section matching a deep-link hash into view on mount", async () => {
    window.location.hash = "#credentials";
    stubRouter();
    render(<SettingsSurface />);

    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());
    const section = document.getElementById("credentials")!;
    expect(section.scrollIntoView).toHaveBeenCalled();
  });

  it.each([
    ["general", "sessions"],
    ["pipeline", "pipelines"],
    ["advanced", "experimental"],
    ["providers", "models"],
    ["updates", "about"],
  ])("follows an old #%s link to #%s", async (old, current) => {
    window.location.hash = `#${old}`;
    stubRouter();
    render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    expect(document.getElementById(current)!.scrollIntoView).toHaveBeenCalled();
    expect(window.location.hash).toBe(`#${current}`);
  });

  it("an old #shells link (the dock's Manage) scrolls to Shells in System and opens the inventory", async () => {
    window.location.hash = "#shells";
    stubRouter();
    render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    expect(document.getElementById("system-shells")!.scrollIntoView).toHaveBeenCalled();
    expect(screen.getByRole("complementary", { name: "Shells" })).toBeTruthy();
    expect(window.location.hash).toBe("#system");
  });

  it("opens no shells sheet without a #shells link", async () => {
    stubRouter();
    render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
    expect(screen.queryByRole("complementary", { name: "Shells" })).toBeNull();
  });

  describe("field map", () => {
    // Inputs whose id isn't their field's kebab-cased name (kept stable for
    // tests and links), and fields with no input on this page.
    const ID_ALIASES: Partial<Record<keyof Config, string[]>> = {
      hide_delegates_in_session_list: ["expose-delegates"],
      allow_sleep_during_pipelines: ["prevent-sleep"],
      env_passthrough: ["env-passthrough-input"],
      feature_flags: ["feature-flag-multi_repo", "feature-flag-linked_sessions", "feature-flag-remote_registry"],
    };
    const LAUNCHPAD_ONLY: (keyof Config)[] = ["default_model", "default_agent", "default_isolation"];
    const idsFor = (field: keyof Config) => ID_ALIASES[field] ?? [field.replaceAll("_", "-")];

    // Each draft reveals a different set of conditional rows.
    const drafts: Partial<Config>[] = [
      { default_workspace: "ws-1", fast_model_mode: "endpoint", stream_speed: "custom", env_mode: "isolated" },
      { default_dir: "/repo" },
    ];

    it("every field's input sits in the section FIELD_SECTION names, and every mapped field has one", async () => {
      const rendered = new Set<string>();
      for (const patch of drafts) {
        stubRouter();
        server.config = { ...fixtureConfig, ...patch };
        const { unmount } = render(<SettingsSurface />);
        await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
        for (const field of Object.keys(fixtureConfig).concat(Object.keys(FIELD_SECTION)) as (keyof Config)[]) {
          for (const id of idsFor(field)) {
            const el = document.getElementById(id);
            if (!el) continue;
            rendered.add(field);
            expect(FIELD_SECTION[field], `${field} has an input but no section`).toBeDefined();
            expect(el.closest("section[id]")?.id, `${field}'s input`).toBe(FIELD_SECTION[field]);
          }
        }
        unmount();
      }
      const missing = (Object.keys(FIELD_SECTION) as (keyof Config)[]).filter(
        (f) => !rendered.has(f) && !LAUNCHPAD_ONLY.includes(f),
      );
      expect(missing).toEqual([]);
    });
  });

  async function renderLoaded() {
    stubRouter();
    render(<SettingsSurface />);
    await waitFor(() => expect(screen.getByLabelText("Concurrency limit")).toBeTruthy());
  }

  const saveBar = () => screen.queryByRole("region", { name: "Unsaved changes" });
  const editConcurrency = (n: number) =>
    fireEvent.change(screen.getByLabelText("Concurrency limit"), { target: { value: String(n) } });

  describe("save bar", () => {
    it("appears with a count once the draft differs from saved, and Discard puts it back", async () => {
      await renderLoaded();
      expect(saveBar()).toBeNull();

      editConcurrency(7);
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Keybindings" })).getByRole("radio", { name: "Vim" }));
      const bar = saveBar()!;
      expect(within(bar).getByText("2 settings")).toBeTruthy();
      expect(within(bar).getByText("Restart needed")).toBeTruthy();

      fireEvent.click(within(bar).getByRole("button", { name: "Discard" }));
      expect(saveBar()).toBeNull();
      expect((screen.getByLabelText("Concurrency limit") as HTMLInputElement).value).toBe(
        String(fixtureConfig.concurrency_limit),
      );
      expect(server.puts).toHaveLength(0);
    });

    it("has no restart tag when no restart-required field is dirty", async () => {
      await renderLoaded();
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Keybindings" })).getByRole("radio", { name: "Vim" }));
      expect(within(saveBar()!).getByText("1 setting")).toBeTruthy();
      expect(within(saveBar()!).queryByText("Restart needed")).toBeNull();
    });

    it("Save persists the draft, hides the bar, and notes a pending restart", async () => {
      await renderLoaded();
      editConcurrency(7);
      fireEvent.click(within(saveBar()!).getByRole("button", { name: "Save" }));

      await waitFor(() => expect(saveBar()).toBeNull());
      expect(server.config.concurrency_limit).toBe(7);
      expect(screen.getByText("Saved. Some changes apply when mads restarts.")).toBeTruthy();
    });

    it("keeps the bar and the draft on a validation failure, and scrolls to the field", async () => {
      await renderLoaded();
      editConcurrency(0);
      server.rejectPut = [{ field: "concurrency_limit", message: "must be at least 1" }];
      const input = screen.getByLabelText("Concurrency limit");
      (input.scrollIntoView as ReturnType<typeof vi.fn>).mockClear();

      fireEvent.click(within(saveBar()!).getByRole("button", { name: "Save" }));

      await waitFor(() => expect(screen.getByText("must be at least 1")).toBeTruthy());
      expect(saveBar()).toBeTruthy();
      expect((input as HTMLInputElement).value).toBe("0");
      await waitFor(() => expect(input.scrollIntoView).toHaveBeenCalled());
    });

    it("never appears for live controls, which persist only their own field", async () => {
      await renderLoaded();
      editConcurrency(7);
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Theme" })).getByRole("radio", { name: "Light" }));
      fireEvent.click(screen.getByRole("switch", { name: "Linked sessions" }));
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Density" })).getByRole("radio", { name: "Compact" }));

      await waitFor(() => expect(server.puts).toHaveLength(3));
      expect(server.config.theme).toBe("light");
      expect(server.config.feature_flags.linked_sessions).toBe(true);
      expect(server.config.display_mode).toBe("compact");
      // The unsaved General edit went along with neither.
      expect(server.config.concurrency_limit).toBe(fixtureConfig.concurrency_limit);
      expect(within(saveBar()!).getByText("1 setting")).toBeTruthy();
    });

    it("picks up Credentials edits, which otherwise had no way to save", async () => {
      await renderLoaded();
      fireEvent.click(
        within(screen.getByRole("radiogroup", { name: "Environment" })).getByRole("radio", { name: "Isolated" }),
      );
      expect(within(saveBar()!).getByText("1 setting")).toBeTruthy();
      fireEvent.click(within(saveBar()!).getByRole("button", { name: "Save" }));
      await waitFor(() => expect(server.config.env_mode).toBe("isolated"));
    });

    it("holds profile switching while there are unsaved edits", async () => {
      await renderLoaded();
      await waitFor(() => expect(screen.getByRole("button", { name: "Switch to this profile" })).toBeTruthy());
      editConcurrency(7);
      expect((screen.getByRole("button", { name: "Switch to this profile" }) as HTMLButtonElement).disabled).toBe(true);
      expect(screen.getByText("Save or discard your changes first to switch profiles.")).toBeTruthy();
    });
  });

  describe("profile switch undo", () => {
    async function switchProfile() {
      await renderLoaded();
      fireEvent.click(await screen.findByRole("button", { name: "Switch to this profile" }));
      fireEvent.click(screen.getByRole("button", { name: "Switch profile" }));
      await screen.findByText("Profile switched");
      await waitFor(() => expect(screen.getByLabelText("Concurrency limit")).toHaveProperty("value", "1"));
    }
    const undo = () => screen.getByRole("button", { name: "Undo" }) as HTMLButtonElement;

    it("puts the previous config back", async () => {
      await switchProfile();
      expect(undo().disabled).toBe(false);
      fireEvent.click(undo());
      await waitFor(() => expect(server.config.concurrency_limit).toBe(fixtureConfig.concurrency_limit));
      expect(server.puts.at(-1)?.default_model_tier).toBe(fixtureConfig.default_model_tier);
    });

    it("waits while there are unsaved edits, so it can't overwrite them", async () => {
      await switchProfile();
      editConcurrency(5);
      expect(undo().disabled).toBe(true);
      expect(screen.getByText(/Save or discard your changes to undo/)).toBeTruthy();
      fireEvent.click(undo());
      expect(server.puts).toHaveLength(0);

      fireEvent.click(within(saveBar()!).getByRole("button", { name: "Discard" }));
      expect(undo().disabled).toBe(false);
    });
  });

  describe("leave guard", () => {
    it("lets a leave through when nothing is unsaved", async () => {
      await renderLoaded();
      const proceed = vi.fn();
      act(() => guardLeave(proceed));
      expect(proceed).toHaveBeenCalled();
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });

    it("holds a leave with unsaved edits; Keep editing stays put", async () => {
      await renderLoaded();
      editConcurrency(7);
      const proceed = vi.fn();
      act(() => guardLeave(proceed));
      expect(proceed).not.toHaveBeenCalled();
      const dialog = screen.getByRole("alertdialog", { name: "Unsaved changes" });

      fireEvent.click(within(dialog).getByRole("button", { name: "Keep editing" }));
      expect(screen.queryByRole("alertdialog")).toBeNull();
      expect(proceed).not.toHaveBeenCalled();
      expect(saveBar()).toBeTruthy();
    });

    it("Save saves, then leaves", async () => {
      await renderLoaded();
      editConcurrency(7);
      const proceed = vi.fn();
      act(() => guardLeave(proceed));
      fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Save" }));

      await waitFor(() => expect(proceed).toHaveBeenCalled());
      expect(server.config.concurrency_limit).toBe(7);
    });

    it("Save that fails validation stays in Settings", async () => {
      await renderLoaded();
      editConcurrency(0);
      server.rejectPut = [{ field: "concurrency_limit", message: "must be at least 1" }];
      const proceed = vi.fn();
      act(() => guardLeave(proceed));
      fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Save" }));

      await waitFor(() => expect(screen.getByText("must be at least 1")).toBeTruthy());
      expect(proceed).not.toHaveBeenCalled();
      expect(saveBar()).toBeTruthy();
    });

    it("Discard drops the edits and leaves", async () => {
      await renderLoaded();
      editConcurrency(7);
      const proceed = vi.fn();
      act(() => guardLeave(proceed));
      fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Discard" }));

      expect(proceed).toHaveBeenCalled();
      expect(server.puts).toHaveLength(0);
    });

    it("is gone once Settings unmounts", async () => {
      stubRouter();
      const { unmount } = render(<SettingsSurface />);
      await waitFor(() => expect(screen.getByLabelText("Concurrency limit")).toBeTruthy());
      editConcurrency(7);
      unmount();
      const proceed = vi.fn();
      guardLeave(proceed);
      expect(proceed).toHaveBeenCalled();
    });
  });
});
