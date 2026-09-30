import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Composer } from "./Composer";
import { clearAllDrafts } from "./composerDrafts";
import { FakeRouterClient } from "../../router/fixtures";
import type { Agent, Model } from "../../router/types";

// Fake Monaco that also drives the real onMount/onChange callbacks
// ExpandedComposer wires up (addCommand for ⌘Enter send / Escape collapse,
// onDidChangeCursorPosition for the round-trip cursor), so those keybindings
// are exercised the same way the real editor's keybinding service would —
// happy-dom can't mount real Monaco (no Worker/canvas), same precedent as
// YamlSourceView.test.tsx/DiffViewer.test.tsx.
const CTRL_CMD = 1 << 11;
const KEY_ENTER = 3;
const KEY_ESCAPE = 9;

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: {
    value: string;
    language?: string;
    onChange?: (value: string) => void;
    onMount?: (editor: unknown, monaco: unknown) => void;
  }) => {
    const commands = new Map<number, () => void>();
    const fakeEditor = {
      addCommand: (keybinding: number, handler: () => void) => {
        commands.set(keybinding, handler);
      },
      focus: () => {},
      setPosition: () => {},
      revealPositionInCenterIfOutsideViewport: () => {},
      getModel: () => ({
        getPositionAt: (offset: number) => ({ lineNumber: 1, column: offset + 1 }),
        getOffsetAt: (pos: { column: number }) => pos.column - 1,
        getValueLength: () => props.value.length,
      }),
      onDidChangeCursorPosition: () => ({ dispose: () => {} }),
    };
    const monaco = { KeyMod: { CtrlCmd: CTRL_CMD }, KeyCode: { Enter: KEY_ENTER, Escape: KEY_ESCAPE } };
    props.onMount?.(fakeEditor, monaco);
    return (
      <textarea
        data-testid="fake-monaco-editor"
        data-language={props.language}
        value={props.value}
        onChange={(e) => props.onChange?.(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
            commands.get(CTRL_CMD | KEY_ENTER)?.();
          }
          if (e.key === "Escape") {
            commands.get(KEY_ESCAPE)?.();
          }
        }}
      />
    );
  },
}));

// React mirrors a controlled textarea's value into a text-node child (an SSR
// carryover), so plain screen.getByText("/agent") can false-positive-match
// the textarea itself when the typed command equals the label under test.
// Scoping to the listbox sidesteps that collision.
function menu() {
  return within(screen.getByRole("listbox"));
}

function fixtureAgent(id: string): Agent {
  return { id, name: id, prompt: "", created_at: "", updated_at: "" };
}

describe("Composer", () => {
  beforeEach(clearAllDrafts);

  it("resolves @ mentionables against the faked Router response set and shows a menu", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    fireEvent.change(textarea, { target: { value: "check @deep" } });

    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());
    expect(screen.getByText("deep-research")).toBeTruthy();
  });

  it("selects a mention candidate, splicing the token into the value and staging it", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "check @deep" } });
    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());

    fireEvent.mouseDown(screen.getByText("deep-research"));

    expect(textarea.value).toBe("check @deep-research ");
    // staged in the injected-content preview, distinct from the menu option
    expect(screen.getAllByText("deep-research").length).toBeGreaterThan(0);
  });

  it("resolves / commands against skill capabilities, badging unhealthy ones", async () => {
    const client = new FakeRouterClient();
    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        agentId="autonomous-executor"
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    fireEvent.change(textarea, { target: { value: "/security" } });

    await waitFor(() => expect(screen.getByText("/security-review")).toBeTruthy());
    expect(screen.getByText("unhealthy")).toBeTruthy();
  });

  it("⌘E toggles between the textarea and the Monaco editor", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });
    // Monaco is lazy-loaded behind Suspense (see ExpandedComposer) — even a
    // mocked module resolves the dynamic import asynchronously, so the first
    // expand in the file must await it rather than query synchronously.
    const editor = await screen.findByTestId("fake-monaco-editor");
    expect(editor.getAttribute("data-language")).toBe("markdown");
    expect(screen.queryByPlaceholderText(/Message the agent/)).toBeNull();

    fireEvent.keyDown(editor, { key: "e", metaKey: true });
    expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
    expect(screen.getByPlaceholderText(/Message the agent/)).toBeTruthy();
  });

  it("the visible expand button toggles the same way as ⌘E, with a tooltip naming the hotkey", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);

    const expandButton = screen.getByRole("button", { name: "Expand editor" });
    expect(expandButton.getAttribute("title")).toBe("Expand editor (Ctrl+E)");
    fireEvent.click(expandButton);

    expect(await screen.findByTestId("fake-monaco-editor")).toBeTruthy();
    const collapseButton = screen.getByRole("button", { name: "Collapse editor" });
    expect(collapseButton.getAttribute("title")).toBe("Collapse editor (Ctrl+E)");
    fireEvent.click(collapseButton);

    expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
  });

  it("round-trips typed text losslessly between collapsed and expanded", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "line one\nline two\t🎉" } });
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });

    const editor = (await screen.findByTestId("fake-monaco-editor")) as HTMLTextAreaElement;
    expect(editor.value).toBe("line one\nline two\t🎉");

    fireEvent.change(editor, { target: { value: "line one\nline two\t🎉 edited" } });
    fireEvent.keyDown(editor, { key: "e", metaKey: true });

    expect(screen.getByPlaceholderText(/Message the agent/)).toHaveProperty(
      "value",
      "line one\nline two\t🎉 edited",
    );
  });

  it("sends from the expanded editor via ⌘Enter", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });

    const editor = (await screen.findByTestId("fake-monaco-editor")) as HTMLTextAreaElement;
    fireEvent.change(editor, { target: { value: "hello from monaco" } });
    fireEvent.keyDown(editor, { key: "Enter", metaKey: true });

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "hello from monaco",
        mentions: [],
        queue: true,
      }),
    );
  });

  it("Escape in the expanded editor collapses back to the textarea", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });

    const editor = await screen.findByTestId("fake-monaco-editor");
    fireEvent.keyDown(editor, { key: "Escape" });

    expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
    expect(screen.getByPlaceholderText(/Message the agent/)).toBeTruthy();
  });

  it("does not resolve @ mentions or / commands while expanded", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });

    const editor = await screen.findByTestId("fake-monaco-editor");
    fireEvent.change(editor, { target: { value: "check @deep" } });

    await new Promise((r) => setTimeout(r, 200));
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("sends the message via the Router client and clears the composer", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "hello there" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "hello there",
        mentions: [],
        queue: true,
      }),
    );
    expect(textarea.value).toBe("");
  });

  // The queue lives in the session process now: the composer always asks to
  // queue, and a mid-turn send comes back "queued" rather than being held
  // here — a local queue died with the pane and the message was never sent.
  it("hands a mid-turn send to the session's queue instead of holding it", async () => {
    const client = new FakeRouterClient();
    const queued = { id: "q_1", text: "next question", queued_at: "2026-09-29T22:58:00Z" };
    const sendMessage = vi
      .spyOn(client, "sendMessage")
      .mockResolvedValue({ status: "queued", queued });
    const onQueued = vi.fn();
    const onSent = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        busy
        onQueued={onQueued}
        onSent={onSent}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "next question" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() => expect(onQueued).toHaveBeenCalledWith(queued));
    expect(sendMessage).toHaveBeenCalledWith("session-active", {
      text: "next question",
      mentions: [],
      queue: true,
    });
    expect(onSent).not.toHaveBeenCalled(); // no user bubble until it's delivered
    expect(textarea.value).toBe("");
  });

  it("shows a Stop button while busy and calls onStop when clicked", () => {
    const client = new FakeRouterClient();
    const onStop = vi.fn();

    const { rerender } = render(
      <Composer routerClient={client} sessionId="session-active" onStop={onStop} />,
    );
    // Idle: no Stop button.
    expect(screen.queryByRole("button", { name: /stop generating/i })).toBeNull();

    rerender(<Composer routerClient={client} sessionId="session-active" busy onStop={onStop} />);
    const stop = screen.getByRole("button", { name: /stop generating/i });
    fireEvent.click(stop);
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("renders the session's queued messages and cancels one by id", () => {
    const onCancelQueued = vi.fn();
    render(
      <Composer
        routerClient={new FakeRouterClient()}
        sessionId="session-active"
        busy
        queued={[
          { id: "q_1", text: "first", queued_at: "2026-09-29T22:58:00Z" },
          { id: "q_2", text: "second", queued_at: "2026-09-29T22:58:05Z" },
        ]}
        onCancelQueued={onCancelQueued}
      />,
    );
    expect(screen.getAllByText("Queued")).toHaveLength(2);
    fireEvent.click(screen.getAllByLabelText("Remove queued message")[1]);
    expect(onCancelQueued).toHaveBeenCalledWith("q_2");
  });

  it("restores a per-session draft after the composer remounts", () => {
    const client = new FakeRouterClient();
    const { unmount } = render(<Composer routerClient={client} sessionId="session-active" />);
    fireEvent.change(screen.getByPlaceholderText(/Message the agent/), {
      target: { value: "half-written thought" },
    });
    unmount();

    render(<Composer routerClient={client} sessionId="session-active" />);
    expect((screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement).value).toBe(
      "half-written thought",
    );
  });

  it("keeps drafts separate per session", () => {
    const client = new FakeRouterClient();
    const { unmount } = render(<Composer routerClient={client} sessionId="session-active" />);
    fireEvent.change(screen.getByPlaceholderText(/Message the agent/), {
      target: { value: "for session A" },
    });
    unmount();

    render(<Composer routerClient={client} sessionId="session-other" />);
    expect((screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement).value).toBe("");
  });

  it("auto-sends initialValue on mount and clears the textarea once it succeeds", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        initialValue="fix the deploy script"
        autoSendInitial
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    expect(textarea.value).toBe("fix the deploy script");

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "fix the deploy script",
        mentions: [],
        queue: true,
      }),
    );
    await waitFor(() => expect(textarea.value).toBe(""));
  });

  it("preserves the initial text and calls onSendError when the auto-send fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(new Error("network down"));
    const onSendError = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        initialValue="fix the deploy script"
        autoSendInitial
        onSendError={onSendError}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    await waitFor(() => expect(onSendError).toHaveBeenCalledWith("network down"));
    expect(textarea.value).toBe("fix the deploy script");
  });

  it("keeps the typed text in the textarea when a manual send fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(new Error("boom"));
    const onSendError = vi.fn();

    render(<Composer routerClient={client} sessionId="session-active" onSendError={onSendError} />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "hello there" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() => expect(onSendError).toHaveBeenCalledWith("boom"));
    expect(textarea.value).toBe("hello there");
  });

  it("'/agent' descends to registry agents; selecting one rebinds the session, clears the composer, and confirms — no chip, no send", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "listAgents").mockResolvedValue([fixtureAgent("investigator")]);
    const sendMessage = vi.spyOn(client, "sendMessage");
    const onUpdateSession = vi.fn();
    const onCommandExecuted = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        onUpdateSession={onUpdateSession}
        onCommandExecuted={onCommandExecuted}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/agent" } });
    await waitFor(() => expect(menu().getByRole("option", { name: "/agent" })).toBeTruthy());

    fireEvent.mouseDown(menu().getByRole("option", { name: "/agent" }));
    expect(textarea.value).toBe("/agent ");

    await waitFor(() =>
      expect(menu().getByRole("option", { name: "investigator" })).toBeTruthy(),
    );
    fireEvent.mouseDown(menu().getByRole("option", { name: "investigator" }));

    expect(onUpdateSession).toHaveBeenCalledWith({ agent: "investigator" });
    await waitFor(() =>
      expect(onCommandExecuted).toHaveBeenCalledWith("Agent set to investigator"),
    );
    expect(textarea.value).toBe("");
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
    expect(sendMessage).not.toHaveBeenCalled();
  });

  it("'/model' descends to the session's provider model list and rebinds on select", async () => {
    const client = new FakeRouterClient();
    const models: Model[] = [{ id: "claude-opus-5", name: "Claude Opus 5" }];
    vi.spyOn(client, "listModels").mockResolvedValue(models);
    const onUpdateSession = vi.fn();
    const onCommandExecuted = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        provider="claudecode"
        onUpdateSession={onUpdateSession}
        onCommandExecuted={onCommandExecuted}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/model" } });
    await waitFor(() => expect(menu().getByRole("option", { name: "/model" })).toBeTruthy());

    fireEvent.mouseDown(menu().getByRole("option", { name: "/model" }));
    expect(textarea.value).toBe("/model ");

    await waitFor(() =>
      expect(menu().getByRole("option", { name: "Claude Opus 5" })).toBeTruthy(),
    );
    fireEvent.mouseDown(menu().getByRole("option", { name: "Claude Opus 5" }));

    expect(client.listModels).toHaveBeenCalledWith("claudecode");
    expect(onUpdateSession).toHaveBeenCalledWith({ model: "claude-opus-5" });
    await waitFor(() =>
      expect(onCommandExecuted).toHaveBeenCalledWith("Model set to Claude Opus 5"),
    );
    expect(textarea.value).toBe("");
  });

  it("a no-argument command (/task) executes on first select — inserts text, stages no chip", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/task" } });
    await waitFor(() => expect(menu().getByRole("option", { name: "/task" })).toBeTruthy());

    fireEvent.mouseDown(menu().getByRole("option", { name: "/task" }));

    expect(textarea.value).toBe("/task ");
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
  });

  it("unrecognized text after / just filters — no menu, no crash, no chip", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/zzzznotacommand" } });
    await new Promise((r) => setTimeout(r, 200));

    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
    expect(textarea.value).toBe("/zzzznotacommand");
  });

  it("drives the two-level command menu entirely by keyboard: open, descend, arrow, back, execute", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "listAgents").mockResolvedValue([
      fixtureAgent("autonomous-executor"),
      fixtureAgent("investigator"),
    ]);
    const onUpdateSession = vi.fn();

    render(
      <Composer routerClient={client} sessionId="session-active" onUpdateSession={onUpdateSession} />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    // open — typing "/" surfaces the root command list
    fireEvent.change(textarea, { target: { value: "/agent" } });
    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());

    // descend — Enter on the "/agent" group advances to level 2
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(textarea.value).toBe("/agent ");
    await waitFor(() =>
      expect(menu().getByRole("option", { name: "investigator" })).toBeTruthy(),
    );

    // arrow — ArrowDown moves off the first leaf onto the second
    fireEvent.keyDown(textarea, { key: "ArrowDown" });

    // back — Escape drops the argument, returning to level 1
    fireEvent.keyDown(textarea, { key: "Escape" });
    expect(textarea.value).toBe("/agent");
    await waitFor(() => expect(menu().getByRole("option", { name: "/agent" })).toBeTruthy());

    // descend again, then execute via Enter on the (reset) first leaf
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(textarea.value).toBe("/agent ");
    await waitFor(() =>
      expect(menu().getByRole("option", { name: "autonomous-executor" })).toBeTruthy(),
    );
    fireEvent.keyDown(textarea, { key: "Enter" });

    expect(onUpdateSession).toHaveBeenCalledWith({ agent: "autonomous-executor" });
    expect(textarea.value).toBe("");
  });
});
