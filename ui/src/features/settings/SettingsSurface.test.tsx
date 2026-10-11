import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsSurface } from "./SettingsSurface";
import { guardLeave } from "../../appNavigation";
import type { Config } from "../../router/types";
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
    expect(screen.getByRole("heading", { name: "Providers" })).toBeTruthy();
    await waitFor(() => expect(screen.getByText(/version/i)).toBeTruthy());
  });

  it("scrolls the section matching a deep-link hash into view on mount", async () => {
    window.location.hash = "#credentials";
    stubRouter();
    render(<SettingsSurface />);

    await waitFor(() => expect(screen.getByText(/no credentials stored/i)).toBeTruthy());
    const section = document.getElementById("credentials")!;
    expect(section.scrollIntoView).toHaveBeenCalled();
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
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Display mode" })).getByRole("radio", { name: "Compact" }));
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
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Display mode" })).getByRole("radio", { name: "Compact" }));
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

      await waitFor(() => expect(server.puts).toHaveLength(2));
      expect(server.config.theme).toBe("light");
      expect(server.config.feature_flags.linked_sessions).toBe(true);
      // The unsaved General edit went along with neither.
      expect(server.config.concurrency_limit).toBe(fixtureConfig.concurrency_limit);
      expect(within(saveBar()!).getByText("1 setting")).toBeTruthy();
    });

    it("picks up Credentials edits, which otherwise had no way to save", async () => {
      await renderLoaded();
      fireEvent.click(
        within(screen.getByRole("radiogroup", { name: "Environment mode" })).getByRole("radio", { name: "Isolated" }),
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
      expect(screen.getByText(/Save or discard your changes first/)).toBeTruthy();
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
