import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState, type ReactNode } from "react";
import { ChangesTab, LIST_HIDDEN_KEY } from "./ChangesTab";
import { FullWidthContext } from "../features/sessions/fullWidth";
import { FakeRouterClient } from "../router/fixtures";
import { FetchRouterClient } from "../router/client";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { ChangeSet } from "../router/types";
import { file } from "./testChangeFile";
import { seenKey } from "./seenState";

vi.mock("@monaco-editor/react", () => ({
  DiffEditor: (props: { original: string; modified: string }) => (
    <div data-testid="fake-diff-editor" data-original={props.original} data-modified={props.modified} />
  ),
  // New and deleted files render as plain code.
  Editor: (props: { value: string }) => <div data-testid="fake-editor" data-value={props.value} />,
}));

const SID = "session-files-clean";

function list() {
  return within(screen.getByTestId("changes-list"));
}

async function inList() {
  return within(await screen.findByTestId("changes-list"));
}

function row(name: string) {
  return list().getByText(name).closest("button")!;
}

function group(by: string) {
  fireEvent.click(screen.getByRole("radio", { name: by }));
}

function changeSet(over: Partial<ChangeSet>): ChangeSet {
  return {
    baseline: "start",
    baselines: [{ id: "start", label: "Session start", available: true }],
    git: true,
    files: [],
    turns: [],
    commits: [],
    totals: { files: 0, added: 0, deleted: 0 },
    computed_at: "2026-10-14T10:00:00Z",
    ...over,
  };
}

describe("ChangesTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("groups by tier by default, filename first with its folder beneath, Scratch collapsed", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    expect(await (await inList()).findByText("Source")).toBeTruthy();
    expect(list().getByText("Hand-written code and docs: read these first")).toBeTruthy();
    expect(list().getByText("Tests & fixtures")).toBeTruthy();
    // Source: biggest first.
    const paths = list()
      .getAllByRole("button")
      .map((b) => b.dataset.path)
      .filter(Boolean);
    expect(paths.slice(0, 3)).toEqual([
      "services/data/internal/legacy_retry.go",
      "services/data/internal/backoff.go",
      "services/data/internal/ingest.go",
    ]);
    expect(row("ingest.go").textContent).toContain("services/data/internal");
    // Scratch is collapsed: its header shows, its file doesn't.
    expect(list().getByText("Scratch")).toBeTruthy();
    expect(list().queryByText("out.json")).toBeNull();
    expect(screen.getByTestId("changes-totals").textContent).toMatch(/^5 files/);
  });

  it("shows a selected file's working diff against the chosen baseline", async () => {
    const client = new FakeRouterClient();
    render(<ChangesTab sessionId={SID} routerClient={client} />);
    fireEvent.click(await (await inList()).findByText("ingest.go"));
    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-original")).toBe("func ingest() {\n\ttime.Sleep(retryDelay)\n}\n");
    expect(client.fileDiffCalls.at(-1)).toEqual({ sessionId: SID, path: "services/data/internal/ingest.go", vs: "start" });
    expect(row("ingest.go").getAttribute("data-selected")).toBe("true");
  });

  it("the detail header carries the badge, path, counts, commit state and turn chips", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    fireEvent.click(await (await inList()).findByText("ingest.go"));
    await screen.findByTestId("fake-diff-editor");
    // Once on the row, once in the detail header.
    expect(screen.getAllByTitle("services/data/internal/ingest.go")).toHaveLength(2);
    expect(screen.getByText("uncommitted")).toBeTruthy();
    expect(screen.getByText("changed in")).toBeTruthy();
    expect(screen.getByText("turn 1").closest("[title]")?.getAttribute("title")).toBe(
      "Add retry backoff with jitter to ingest",
    );
    expect(screen.getByRole("button", { name: "Open in editor" })).toBeTruthy();
  });

  it("auto-selects focusPath (the Commit dialog's link)", async () => {
    render(
      <ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} focusPath="services/data/internal/ingest.go" />,
    );
    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-original")).toBe("func ingest() {\n\ttime.Sleep(retryDelay)\n}\n");
  });

  it("focusCommit opens the Commit grouping on that commit's file and diff (the Land card)", async () => {
    render(
      <ChangesTab
        sessionId={SID}
        routerClient={new FakeRouterClient()}
        focusCommit={{ hash: "a1b2c3d", path: "services/data/internal/backoff.go" }}
      />,
    );
    // A file the commit added: plain code, not a diff against nothing.
    const editor = await screen.findByTestId("fake-editor");
    expect(editor.getAttribute("data-value")).toBe("func backoff() time.Duration {\n\treturn baseDelay\n}\n");
    expect(screen.getByText(/New file · 3 lines/)).toBeTruthy();
    expect(screen.getByText("Diff from commit a1b2c3d")).toBeTruthy();
    expect(screen.getByRole("radio", { name: "Commit" }).getAttribute("aria-checked")).toBe("true");
    expect(list().getByText("ingest: add retry backoff with jitter")).toBeTruthy();
  });

  it("Commit grouping: Uncommitted first, then commits; a commit's row shows that commit's diff", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    await (await inList()).findByText("Source");
    group("Commit");
    const headers = list()
      .getAllByRole("button", { expanded: true })
      .map((b) => b.textContent);
    expect(headers[0]).toMatch(/^▾Uncommitted/);
    expect(headers[1]).toMatch(/ingest: add retry backoff with jitter/);
    fireEvent.click(row("backoff.go"));
    expect(await screen.findByText("Diff from commit a1b2c3d")).toBeTruthy();
  });

  it("the header's commit link jumps to the Commit grouping", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    fireEvent.click(await (await inList()).findByText("backoff.go"));
    expect(await screen.findByText("committed in")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "a1b2c3d" }));
    expect(screen.getByRole("radio", { name: "Commit" }).getAttribute("aria-checked")).toBe("true");
    expect(await screen.findByText("Diff from commit a1b2c3d")).toBeTruthy();
  });

  it("Intent is always disabled; Commit is disabled outside git", async () => {
    const client = new FakeRouterClient();
    client.changeSets.set("s-nogit", changeSet({ git: false, files: [file("a.go")] }));
    render(<ChangesTab sessionId="s-nogit" routerClient={client} />);
    await (await inList()).findByText("a.go");
    const intent = screen.getByRole("radio", { name: "Intent" }) as HTMLButtonElement;
    expect(intent.disabled).toBe(true);
    expect(intent.title).toBe("Needs a grouping plug-in");
    expect((screen.getByRole("radio", { name: "Commit" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("the turn strip filters: click a turn (its prompt pins on the context line), click it again to clear", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    await (await inList()).findByText("Source");
    const ctx = screen.getByTestId("turn-context");
    expect(ctx.textContent).toMatch(/All turns/i);

    fireEvent.click(screen.getByRole("button", { name: "Turn 2, 3 files" }));
    expect(ctx.textContent).toContain("“Drop the legacy retry and test the new one”");
    expect(list().queryByText("ingest.go")).toBeNull();
    expect(list().getByText("legacy_retry.go")).toBeTruthy();
    expect(screen.getByTestId("changes-totals").textContent).toMatch(/^3 of 5 files/);

    fireEvent.click(screen.getByRole("button", { name: "Turn 2, 3 files" }));
    expect(list().getByText("ingest.go")).toBeTruthy();
  });

  it("shift-click selects a range of turns", async () => {
    const client = new FakeRouterClient();
    client.changeSets.set(
      "s-turns",
      changeSet({
        files: [file("a.go", { turns: [1] }), file("b.go", { turns: [2] }), file("c.go", { turns: [3] })],
        turns: [1, 2, 3].map((n) => ({ n, prompt: `p${n}`, at: "", files: 1 })),
      }),
    );
    render(<ChangesTab sessionId="s-turns" routerClient={client} />);
    await (await inList()).findByText("a.go");
    fireEvent.click(screen.getByRole("button", { name: "Turn 2, 1 file" }));
    fireEvent.click(screen.getByRole("button", { name: "Turn 3, 1 file" }), { shiftKey: true });
    expect(list().queryByText("a.go")).toBeNull();
    expect(list().getByText("b.go")).toBeTruthy();
    expect(list().getByText("c.go")).toBeTruthy();
    expect(screen.getByTestId("turn-context").textContent).toMatch(/Turns 2–3/i);
  });

  it("a turn chip in the header filters to that turn", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    fireEvent.click(await (await inList()).findByText("legacy_retry.go"));
    fireEvent.click(await screen.findByRole("button", { name: "turn 2" }));
    expect(screen.getByRole("button", { name: "Turn 2, 3 files" }).getAttribute("aria-pressed")).toBe("true");
    expect(list().queryByText("ingest.go")).toBeNull();
  });

  it("Turn grouping: a section per turn, files under every turn that touched them; the strip navigates", async () => {
    const client = new FakeRouterClient();
    client.changeSets.set(
      "s-turns",
      changeSet({
        files: [file("a.go", { turns: [1, 3] }), file("b.go", { turns: [1] })],
        turns: [
          { n: 1, prompt: "first", at: "", files: 2 },
          { n: 2, prompt: "", at: "", files: 0 },
          { n: 3, prompt: "third", at: "", files: 1 },
        ],
      }),
    );
    render(<ChangesTab sessionId="s-turns" routerClient={client} />);
    await (await inList()).findByText("a.go");
    group("Turn");
    expect(list().getByText("Turn 1")).toBeTruthy();
    expect(list().getByText("no file changes")).toBeTruthy();
    // An empty prompt (defensive; every turn has one) is just "Turn N", no explanation.
    expect(list().getByText("Turn 2").parentElement!.textContent).toBe("Turn 2");
    expect(list().getAllByText("a.go")).toHaveLength(2);
    // Navigation, not filtering: the column lights up as "in view" and nothing is hidden.
    fireEvent.click(screen.getByRole("button", { name: "Turn 3, 1 file" }));
    expect(screen.getByRole("button", { name: "Turn 3, 1 file" }).getAttribute("aria-current")).toBe("true");
    expect(list().getByText("b.go")).toBeTruthy();
    expect(screen.getByTestId("turn-context").textContent).toContain("third");
  });

  it("Folder grouping is a tree with an outside-the-folder root", async () => {
    const client = new FakeRouterClient();
    client.changeSets.set(
      "s-tree",
      changeSet({
        files: [
          file("internal/auth/a.go"),
          file("internal/auth/b.go"),
          file("/tmp/x.log", { path: "/tmp/x.log", display: "/tmp/x.log", outside: true, tier: "scratch" }),
        ],
      }),
    );
    render(<ChangesTab sessionId="s-tree" routerClient={client} />);
    await (await inList()).findByText("a.go");
    group("Folder");
    expect(list().getByText("internal/auth")).toBeTruthy();
    expect(list().getByText("Outside the session folder")).toBeTruthy();
    // Only-scratch folders start collapsed.
    expect(list().queryByText("x.log")).toBeNull();
  });

  it("deletes scratch after a confirmation listing each path and rule, then refetches and reports", async () => {
    const client = new FakeRouterClient();
    render(<ChangesTab sessionId={SID} routerClient={client} />);
    await (await inList()).findByText("Source");
    fireEvent.click(screen.getByRole("button", { name: "Delete 1…" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Delete 1 scratch file?")).toBeTruthy();
    expect(dialog.textContent).toContain("out.json");
    expect(dialog.textContent).toContain("never referenced");
    expect(dialog.textContent).toContain("This can't be undone.");

    const callsBefore = client.changeSetCalls.length;
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete files" }));
    expect(await screen.findByText("Deleted 1 scratch file")).toBeTruthy();
    expect(client.deleteScratchCalls).toEqual([{ sessionId: SID, paths: ["out.json"] }]);
    await waitFor(() => expect(client.changeSetCalls.length).toBeGreaterThan(callsBefore));
    await waitFor(() => expect(list().queryByText("Scratch")).toBeNull());
  });

  it("reports scratch files the server skipped", async () => {
    const client = new FakeRouterClient();
    client.deleteScratchOutcome = { deleted: [], skipped: [{ path: "out.json", reason: "now referenced by ingest.go" }] };
    render(<ChangesTab sessionId={SID} routerClient={client} />);
    await (await inList()).findByText("Source");
    fireEvent.click(screen.getByRole("button", { name: "Delete 1…" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete files" }));
    expect(await screen.findByText("Deleted 0 scratch files, skipped 1")).toBeTruthy();
    expect(screen.getByText("out.json: now referenced by ingest.go")).toBeTruthy();
  });

  it("cancelling the confirmation deletes nothing", async () => {
    const client = new FakeRouterClient();
    render(<ChangesTab sessionId={SID} routerClient={client} />);
    await (await inList()).findByText("Source");
    fireEvent.click(screen.getByRole("button", { name: "Delete 1…" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(client.deleteScratchCalls).toEqual([]);
  });

  it("shows the baseline switcher only with more than one available baseline, and refetches on switch", async () => {
    const client = new FakeRouterClient();
    client.changeSets.set(
      "s-vs",
      changeSet({
        files: [file("a.go")],
        baselines: [
          { id: "start", label: "Session start", available: true },
          { id: "base", label: "Base branch · feat/x", available: true },
          { id: "main", label: "main", available: false, reason: "same as base" },
        ],
      }),
    );
    render(<ChangesTab sessionId="s-vs" routerClient={client} />);
    await (await inList()).findByText("a.go");
    expect(screen.queryByRole("radio", { name: "main" })).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: "Base branch · feat/x" }));
    await waitFor(() => expect(client.changeSetCalls.at(-1)).toEqual({ sessionId: "s-vs", vs: "base" }));
    fireEvent.click(await (await inList()).findByText("a.go"));
    await waitFor(() => expect(client.fileDiffCalls.at(-1)?.vs).toBe("base"));
  });

  it("hides the baseline switcher when only session start applies", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    await (await inList()).findByText("Source");
    expect(screen.queryByText("Compare to")).toBeNull();
  });

  it("shows notes as a quiet line", async () => {
    const client = new FakeRouterClient();
    client.changeSets.set("s-notes", changeSet({ files: [file("a.go")], notes: ["baseline snapshot still running"] }));
    render(<ChangesTab sessionId="s-notes" routerClient={client} />);
    expect(await screen.findByText("baseline snapshot still running")).toBeTruthy();
  });

  it("says No changes yet for an empty set, and hides the strip with no turns", async () => {
    render(<ChangesTab sessionId="session-none" routerClient={new FakeRouterClient()} />);
    expect(await (await inList()).findByText("No changes yet")).toBeTruthy();
    expect(screen.queryByTestId("turn-context")).toBeNull();
  });

  it("shows a load error with a retry", async () => {
    const client = new FakeRouterClient();
    let fail = true;
    client.changeSetOutcome = async () => {
      if (fail) throw new Error("router unreachable");
      return changeSet({ files: [file("a.go")] });
    };
    render(<ChangesTab sessionId="s-err" routerClient={client} />);
    expect(await screen.findByText(/Couldn't load changes: router unreachable/)).toBeTruthy();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await (await inList()).findByText("a.go")).toBeTruthy();
  });

  it("filters by the filter box", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    await (await inList()).findByText("Source");
    fireEvent.change(screen.getByLabelText("Filter files"), { target: { value: "legacy" } });
    expect(list().getByText("legacy_retry.go")).toBeTruthy();
    expect(list().queryByText("ingest.go")).toBeNull();
  });

  it("marks files new until selected, per session in localStorage; headers dot while a child is new", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    await (await inList()).findByText("Source");
    const ingest = row("ingest.go");
    expect(within(ingest).queryByLabelText("new since you looked")).toBeTruthy();
    fireEvent.click(ingest);
    await waitFor(() => expect(within(row("ingest.go")).queryByLabelText("new since you looked")).toBeNull());
    expect(JSON.parse(localStorage.getItem(seenKey(SID))!)).toHaveProperty(["services/data/internal/ingest.go"]);
    const sourceHeader = list().getByText("Source").closest("button")!;
    expect(within(sourceHeader).queryByLabelText("new since you looked")).toBeTruthy();
  });

  it("refetches (debounced) on live events and says when the selected file left the set", async () => {
    const client = new FakeRouterClient();
    render(<ChangesTab sessionId={SID} routerClient={client} />);
    fireEvent.click(await (await inList()).findByText("ingest.go"));
    await screen.findByTestId("fake-diff-editor");

    const cs = client.changeSets.get(SID)!;
    cs.files = cs.files.filter((f) => f.path !== "services/data/internal/ingest.go");
    const before = client.changeSetCalls.length;
    vi.useFakeTimers();
    act(() => {
      for (const s of FakeEventSource.instances)
        s.emit("file.changed", JSON.stringify({ seq: 9001, type: "file.changed", session_id: SID }));
    });
    expect(client.changeSetCalls.length).toBe(before);
    await act(async () => {
      vi.advanceTimersByTime(1600);
    });
    vi.useRealTimers();
    await waitFor(() => expect(client.changeSetCalls.length).toBe(before + 1));
    expect(await screen.findByText(/no longer differs from the baseline/)).toBeTruthy();
    expect(list().queryByText("ingest.go")).toBeNull();
  });

  it("focusView applies the rail's tier and turn and selects the file", async () => {
    const client = new FakeRouterClient();
    const { rerender } = render(<ChangesTab sessionId={SID} routerClient={client} />);
    await (await inList()).findByText("Source");
    rerender(<ChangesTab sessionId={SID} routerClient={client} focusView={{ tier: "scratch", turn: 2 }} />);
    // Scratch opens; turn 2 filters.
    expect(await (await inList()).findByText("out.json")).toBeTruthy();
    expect(list().queryByText("ingest.go")).toBeNull();
    expect(screen.getByRole("button", { name: "Turn 2, 3 files" }).getAttribute("aria-pressed")).toBe("true");

    rerender(
      <ChangesTab
        sessionId={SID}
        routerClient={client}
        focusView={{ path: "services/data/internal/ingest.go", tier: "source", turn: null }}
      />,
    );
    await waitFor(() => expect(row("ingest.go").getAttribute("data-selected")).toBe("true"));
    expect(await screen.findByTestId("fake-diff-editor")).toBeTruthy();
  });

  describe("search", () => {
    function tsClient() {
      const client = new FakeRouterClient();
      client.changeSets.set(
        "s-ts",
        changeSet({
          files: [file("ui/model.ts"), file("ui/ChangesTab.tsx"), file("ui/ChangesList.tsx")],
        }),
      );
      return client;
    }

    it("shows whole-token matches first and reveals the partial ones on request", async () => {
      render(<ChangesTab sessionId="s-ts" routerClient={tsClient()} />);
      await (await inList()).findByText("model.ts");
      fireEvent.change(screen.getByLabelText("Filter files"), { target: { value: ".ts" } });
      expect(list().queryByText("ChangesTab.tsx")).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "show 2 partial matches" }));
      expect(list().getByText("ChangesTab.tsx")).toBeTruthy();
      expect(list().getByText("model.ts")).toBeTruthy();
      // A new query starts back at whole-token matches.
      fireEvent.change(screen.getByLabelText("Filter files"), { target: { value: "model.ts" } });
      fireEvent.change(screen.getByLabelText("Filter files"), { target: { value: ".ts" } });
      expect(list().queryByText("ChangesTab.tsx")).toBeNull();
    });
  });

  describe("list column", () => {
    it("hides and shows with the buttons and the \\ key, and remembers it", async () => {
      const { unmount } = render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
      await (await inList()).findByText("Source");
      fireEvent.click(screen.getByRole("button", { name: "Hide the file list" }));
      expect(screen.getByTestId("changes-list").hidden).toBe(true);
      expect(localStorage.getItem(LIST_HIDDEN_KEY)).toBe("1");
      unmount();

      render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
      await screen.findByRole("button", { name: "Show the file list" });
      expect(screen.getByTestId("changes-list").hidden).toBe(true);
      fireEvent.keyDown(document, { key: "\\" });
      expect(screen.getByTestId("changes-list").hidden).toBe(false);
    });

    it("ignores its keys while typing or when the tab isn't showing", async () => {
      const { rerender } = render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
      await (await inList()).findByText("Source");
      fireEvent.keyDown(screen.getByLabelText("Filter files"), { key: "\\" });
      expect(screen.getByTestId("changes-list").hidden).toBe(false);
      rerender(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} active={false} />);
      fireEvent.keyDown(document, { key: "\\" });
      expect(screen.getByTestId("changes-list").hidden).toBe(false);
    });

    it("j / k step the selection through the rows as listed", async () => {
      render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
      await (await inList()).findByText("Source");
      const rows = [...screen.getByTestId("changes-list").querySelectorAll<HTMLElement>("[data-path]")];
      expect(rows.length).toBeGreaterThan(1);
      fireEvent.keyDown(document, { key: "j" });
      await waitFor(() => expect(rows[0].getAttribute("data-selected")).toBe("true"));
      fireEvent.keyDown(document, { key: "j" });
      await waitFor(() => expect(rows[1].getAttribute("data-selected")).toBe("true"));
      fireEvent.keyDown(document, { key: "k" });
      await waitFor(() => expect(rows[0].getAttribute("data-selected")).toBe("true"));
    });
  });

  describe("Full-width", () => {
    let full = false;
    function Host({ children }: { children: ReactNode }) {
      const [on, setOn] = useState(false);
      full = on;
      return <FullWidthContext.Provider value={{ full: on, setFull: setOn }}>{children}</FullWidthContext.Provider>;
    }
    function setWidth(w: number) {
      Object.defineProperty(window, "innerWidth", { configurable: true, value: w });
    }
    afterEach(() => setWidth(1024));

    it("opens Full-width on a small window; the button and Esc give the sidebar back", async () => {
      setWidth(1466);
      render(
        <Host>
          <ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />
        </Host>,
      );
      await (await inList()).findByText("Source");
      expect(full).toBe(true);
      fireEvent.click(screen.getByRole("button", { name: "Half-width" }));
      expect(full).toBe(false);
      fireEvent.click(screen.getByRole("button", { name: "Full-width" }));
      expect(full).toBe(true);
      fireEvent.keyDown(document, { key: "Escape" });
      expect(full).toBe(false);
    });

    it("stays half-width on a large window until asked", async () => {
      setWidth(1920);
      render(
        <Host>
          <ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />
        </Host>,
      );
      await (await inList()).findByText("Source");
      expect(full).toBe(false);
      fireEvent.click(screen.getByRole("button", { name: "Full-width" }));
      expect(full).toBe(true);
    });

    it("gives the sidebar back when the tab is left or unmounted", async () => {
      setWidth(1466);
      const ui = (active: boolean) => (
        <Host>
          <ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} active={active} />
        </Host>
      );
      const { rerender, unmount } = render(ui(true));
      await (await inList()).findByText("Source");
      expect(full).toBe(true);
      rerender(ui(false));
      expect(full).toBe(false);
      rerender(ui(true));
      expect(full).toBe(true);
      // Esc only applies to the tab that's showing.
      rerender(ui(false));
      fireEvent.keyDown(document, { key: "Escape" });
      rerender(ui(true));
      expect(full).toBe(true);
      // Switching sessions or opening a delegate unmounts the tab.
      rerender(<Host>{null}</Host>);
      expect(full).toBe(false);
      unmount();
    });

    it("has no Full-width control outside the sessions surface", async () => {
      render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
      await (await inList()).findByText("Source");
      expect(screen.queryByRole("button", { name: /-width$/ })).toBeNull();
    });
  });

  it("has no merge-back flow, and the retired components stay gone", async () => {
    render(<ChangesTab sessionId={SID} routerClient={new FakeRouterClient()} />);
    await (await inList()).findByText("Source");
    expect(screen.queryByRole("button", { name: "Merge back" })).toBeNull();
    expect(Object.keys(import.meta.glob("./MergeBack/**"))).toEqual([]);
    expect(Object.keys(import.meta.glob("./ChangeList/**"))).toEqual([]);
    expect("mergeSession" in new FakeRouterClient()).toBe(false);
    expect("mergeSession" in FetchRouterClient.prototype).toBe(false);
  });
});
