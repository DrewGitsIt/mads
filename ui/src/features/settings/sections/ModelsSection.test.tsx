import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ModelsSection } from "./ModelsSection";
import { fixtureConfig } from "../../../router/fixtures";
import type { BehaviorInfo, ProviderInfo } from "../../../router/types";
import type { UseConfig } from "../state/useConfig";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    saveFields: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

const behaviors: BehaviorInfo[] = [
  { behavior: "interrupt", level: "required" },
  { behavior: "resume", level: "expected" },
  { behavior: "branching", level: "optional" },
  { behavior: "delegate_lifecycle", level: "optional" },
  { behavior: "trust_read_only", level: "expected" },
];

const providers: ProviderInfo[] = [
  {
    name: "claude-code",
    matrix: {
      interrupt: "emulated",
      resume: "native",
      branching: "native",
      delegate_lifecycle: "native",
      trust_read_only: "native",
    },
  },
  {
    name: "goose",
    matrix: {
      interrupt: "native",
      resume: "native",
      branching: "unsupported",
      delegate_lifecycle: "unsupported",
      trust_read_only: "emulated",
    },
  },
  {
    name: "all-native",
    matrix: {
      interrupt: "native",
      resume: "native",
      branching: "native",
      delegate_lifecycle: "native",
      trust_read_only: "native",
    },
  },
];

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

let fastModelStatus: unknown;
let credentialNames: string[];

beforeEach(() => {
  fastModelStatus = { mode: "provider", provider_name: "claude-code", provider_completer: true };
  credentialNames = [];
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
      if (url.includes("/fast-model/status")) return Promise.resolve(jsonResponse(fastModelStatus));
      if (url.includes("/providers/behaviors")) return Promise.resolve(jsonResponse(behaviors));
      if (url.includes("/providers")) return Promise.resolve(jsonResponse(providers));
      if (url.includes("/credentials/env-candidates"))
        return Promise.resolve(jsonResponse({ candidates: [], looked_for: [] }));
      if (url.includes("/credentials")) return Promise.resolve(jsonResponse(credentialNames));
      throw new Error(`unhandled fetch: ${url}`);
    }),
  );
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const withProvider = (default_provider: string) => makeConfig({ draft: { ...fixtureConfig, default_provider } });

describe("ModelsSection", () => {
  describe("default provider", () => {
    it("selecting a provider calls setDraft", async () => {
      const config = makeConfig();
      render(<ModelsSection config={config} />);
      fireEvent.click(screen.getByLabelText("Default provider"));
      await waitFor(() => expect(screen.getByRole("option", { name: "goose" })).toBeTruthy());
      fireEvent.click(screen.getByRole("option", { name: "goose" }));
      expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ default_provider: "goose" }));
    });

    it("names a provider by its product where it has one, and still saves the registry id", async () => {
      const config = makeConfig();
      render(<ModelsSection config={config} />);
      fireEvent.click(screen.getByLabelText("Default provider"));
      await waitFor(() => expect(screen.getByRole("option", { name: "Claude Code" })).toBeTruthy());
      fireEvent.click(screen.getByRole("option", { name: "Claude Code" }));
      expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ default_provider: "claude-code" }));
    });

    it("tags what the provider doesn't do natively: unsupported as bad, emulated as warn", async () => {
      render(<ModelsSection config={withProvider("goose")} />);
      const tags = await screen.findByTestId("provider-capability-tags");
      const shown = within(tags)
        .getAllByText(/./, { selector: "span" })
        .map((t) => t.textContent);
      expect(shown).toEqual(["No branching", "No subagent cards", "Read-only trust emulated"]);
      expect(within(tags).getByText("No branching").className).toMatch(/bad/);
      expect(within(tags).getByText("Read-only trust emulated").className).toMatch(/warn/);
      expect(within(tags).getByRole("button", { name: "All capabilities →" })).toBeTruthy();
    });

    it("shows at most three tags, No … first, then a +N more that opens the matrix", async () => {
      const patchy: ProviderInfo = {
        name: "patchy",
        matrix: {
          interrupt: "emulated",
          resume: "unsupported",
          branching: "unsupported",
          delegate_lifecycle: "emulated",
          trust_read_only: "unsupported",
        },
      };
      const fetchMock = vi.mocked(fetch);
      const base = fetchMock.getMockImplementation()!;
      fetchMock.mockImplementation((input) =>
        input.toString().endsWith("/providers")
          ? Promise.resolve(jsonResponse([...providers, patchy]))
          : base(input),
      );
      render(<ModelsSection config={withProvider("patchy")} />);
      const tags = await screen.findByTestId("provider-capability-tags");
      expect(
        within(tags)
          .getAllByText(/./, { selector: "span" })
          .map((t) => t.textContent),
      ).toEqual(["No resume", "No branching", "No read-only trust"]);
      const details = document.getElementById("provider-capabilities") as HTMLDetailsElement;
      fireEvent.click(within(tags).getByRole("button", { name: "+2 more" }));
      expect(details.open).toBe(true);
      expect(within(tags).getByRole("button", { name: "All capabilities →" })).toBeTruthy();
    });

    it("hides native behaviours: an all-native provider gets no tags, just the link", async () => {
      render(<ModelsSection config={withProvider("all-native")} />);
      const tags = await screen.findByTestId("provider-capability-tags");
      expect(within(tags).queryAllByText(/./, { selector: "span" })).toHaveLength(0);
      expect(within(tags).getByRole("button", { name: "All capabilities →" })).toBeTruthy();
    });

    it("shows nothing under the picker when no provider is selected", async () => {
      render(<ModelsSection config={makeConfig()} />);
      await screen.findByText(/3 providers/);
      expect(screen.queryByTestId("provider-capability-tags")).toBeNull();
      expect(screen.queryByRole("button", { name: "All capabilities →" })).toBeNull();
    });

    it("All capabilities opens the matrix and scrolls to it", async () => {
      render(<ModelsSection config={withProvider("goose")} />);
      const details = document.getElementById("provider-capabilities") as HTMLDetailsElement;
      expect(details.open).toBe(false);
      fireEvent.click(await screen.findByRole("button", { name: "All capabilities →" }));
      expect(details.open).toBe(true);
      expect(details.scrollIntoView).toHaveBeenCalled();
    });
  });

  describe("provider capabilities", () => {
    it("is collapsed by default and counts providers and behaviours", async () => {
      render(<ModelsSection config={makeConfig()} />);
      expect(await screen.findByText("3 providers · 5 behaviours")).toBeTruthy();
      expect((document.getElementById("provider-capabilities") as HTMLDetailsElement).open).toBe(false);
    });

    it("shows differences only by default, with readable names and support", async () => {
      render(<ModelsSection config={makeConfig()} />);
      await screen.findByText("3 providers · 5 behaviours");
      const table = screen.getByRole("table");
      const rowNames = within(table)
        .getAllByRole("rowheader")
        .map((th) => th.firstChild?.textContent);
      // resume is native everywhere, so it's hidden.
      expect(rowNames).toEqual(["Interrupt", "Branching", "Subagent cards", "Read-only trust"]);
      expect(within(table).getAllByText("Native").length).toBeGreaterThan(0);
      expect(within(table).getAllByText("Emulated").length).toBeGreaterThan(0);
      expect(within(table).getAllByText("No").length).toBeGreaterThan(0);
      expect(within(table).getAllByText("optional").length).toBeGreaterThan(0);
    });

    it("All shows every behaviour", async () => {
      render(<ModelsSection config={makeConfig()} />);
      await screen.findByText("3 providers · 5 behaviours");
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Show:" })).getByRole("radio", { name: "All" }));
      expect(within(screen.getByRole("table")).getAllByRole("rowheader")).toHaveLength(5);
      expect(within(screen.getByRole("table")).getByText("Resume")).toBeTruthy();
    });
  });

  describe("Fast Model", () => {
    const status = () => screen.getByTestId("fast-model-status");

    it("defaults to provider mode and reports active (ok) when the provider supports Completer", async () => {
      render(<ModelsSection config={makeConfig()} />);
      const runsOn = screen.getByRole("radiogroup", { name: "Runs on" });
      expect(within(runsOn).getByRole("radio", { name: "My provider" }).getAttribute("aria-checked")).toBe("true");
      await waitFor(() => expect(status().textContent).toMatch(/running via claude-code/));
      expect(status().getAttribute("data-tone")).toBe("ok");
    });

    it("is bad, naming the provider, when it can't do completions", async () => {
      fastModelStatus = { mode: "provider", provider_name: "kiro-cli", provider_completer: false };
      render(<ModelsSection config={makeConfig()} />);
      await waitFor(() =>
        expect(status().textContent).toBe(
          "kiro-cli can't do background completions. Use an API endpoint or switch provider. Until then, these are skipped: titles, summaries, digests, retros, commit messages.",
        ),
      );
      expect(status().getAttribute("data-tone")).toBe("bad");
    });

    it("is warn while dormant in endpoint mode without an endpoint", () => {
      render(<ModelsSection config={makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } })} />);
      expect(status().textContent).toMatch(/No endpoint yet/);
      expect(status().getAttribute("data-tone")).toBe("warn");
    });

    it("is ok once an endpoint is set", () => {
      render(
        <ModelsSection
          config={makeConfig({ draft: { ...fixtureConfig, fast_model_base_url: "https://api.example.com/v1" } })}
        />,
      );
      expect(status().textContent).toBe("Fast Model is active.");
      expect(status().getAttribute("data-tone")).toBe("ok");
    });

    it("is muted when off", () => {
      render(<ModelsSection config={makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "off" } })} />);
      expect(status().textContent).toMatch(/^Fast Model is off/);
      expect(status().getAttribute("data-tone")).toBe("muted");
    });

    it("editing the endpoint updates the draft", () => {
      const config = makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } });
      render(<ModelsSection config={config} />);
      fireEvent.change(screen.getByLabelText("Endpoint"), { target: { value: "https://api.example.com/v1" } });
      expect(config.setDraft).toHaveBeenCalledWith(
        expect.objectContaining({ fast_model_base_url: "https://api.example.com/v1" }),
      );
    });

    it("hides endpoint/model/credential fields outside endpoint mode", () => {
      render(<ModelsSection config={makeConfig()} />);
      expect(screen.queryByLabelText("Endpoint")).toBeNull();
    });

    it("switching Runs on to Off updates the draft", () => {
      const config = makeConfig();
      render(<ModelsSection config={config} />);
      fireEvent.click(within(screen.getByRole("radiogroup", { name: "Runs on" })).getByRole("radio", { name: "Off" }));
      expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ fast_model_mode: "off" }));
    });

    it("lists stored credentials in the credential picker", async () => {
      credentialNames = ["OPENAI_API_KEY"];
      render(<ModelsSection config={makeConfig({ draft: { ...fixtureConfig, fast_model_mode: "endpoint" } })} />);
      fireEvent.click(screen.getByLabelText("Credential"));
      await waitFor(() => expect(screen.getByRole("option", { name: "OPENAI_API_KEY" })).toBeTruthy());
    });
  });
});
