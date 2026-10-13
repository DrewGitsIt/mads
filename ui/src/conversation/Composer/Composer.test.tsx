import { afterEach, describe, expect, it, vi, beforeEach } from "vitest";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/react";
import { Composer } from "./Composer";
import { clearAllDrafts } from "./composerDrafts";
import { FakeRouterClient } from "../../router/fixtures";
import { setPlatform } from "../../platform/shortcut";
import type { Agent, Model, Session } from "../../router/types";
import type { CommandDescriptor } from "./commands/types";
import { useState } from "react";
import { Popover } from "../../components/Popover/Popover";

// Fake Monaco that also drives the real onMount/onChange callbacks
// ExpandedComposer wires up (addCommand for the sendFromEditor binding and
// Escape collapse, onDidChangeCursorPosition for the round-trip cursor), so
// those keybindings are exercised the same way the real editor's keybinding
// service would — happy-dom can't mount real Monaco (no Worker/canvas), same
// precedent as YamlSourceView.test.tsx/DiffViewer.test.tsx.
const CTRL_CMD = 1 << 11;
const SHIFT = 1 << 10;
const KEY_ENTER = 3;
const KEY_ESCAPE = 9;

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: {
    value: string;
    language?: string;
    options?: { lineNumbers?: string };
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
        getPositionAt: (offset: number) => ({
          lineNumber: 1,
          column: offset + 1,
        }),
        getOffsetAt: (pos: { column: number }) => pos.column - 1,
        getValueLength: () => props.value.length,
      }),
      onDidChangeCursorPosition: () => ({ dispose: () => {} }),
    };
    const monaco = {
      KeyMod: { CtrlCmd: CTRL_CMD, Shift: SHIFT },
      KeyCode: { Enter: KEY_ENTER, Escape: KEY_ESCAPE },
    };
    props.onMount?.(fakeEditor, monaco);
    return (
      <textarea
        data-testid="fake-monaco-editor"
        data-language={props.language}
        data-line-numbers={props.options?.lineNumbers}
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

const PLACEHOLDER = "Message the agent — @ to mention, / for commands";

// Linux/Windows defaults (hotkeys.ts): never a plain Ctrl+letter.
const EXPAND_LINUX = { key: "E", ctrlKey: true, shiftKey: true };
const ATTACH_LINUX = { key: "A", ctrlKey: true, shiftKey: true };
const TOGGLE_VIEW_LINUX = { key: "T", ctrlKey: true, shiftKey: true };

// React mirrors a controlled textarea's value into a text-node child (an SSR
// carryover), so plain screen.getByText("/agent") can false-positive-match
// the textarea itself when the typed command equals the label under test.
// Scoping to the listbox sidesteps that collision.
function menu() {
  return within(screen.getByRole("listbox"));
}

function openPlusMenu() {
  fireEvent.click(screen.getByRole("button", { name: "Input options" }));
  return within(screen.getByRole("menu", { name: "Input options" }));
}

function fixtureAgent(id: string): Agent {
  return { id, name: id, prompt: "", created_at: "", updated_at: "" };
}

describe("Composer", () => {
  beforeEach(() => {
    clearAllDrafts();
    setPlatform("linux");
  });
  afterEach(() => setPlatform(undefined));

  it("keeps the placeholder text exactly (e2e specs select on it)", () => {
    render(
      <Composer
        routerClient={new FakeRouterClient()}
        sessionId="session-active"
      />,
    );
    expect(screen.getByPlaceholderText(PLACEHOLDER)).toBeTruthy();
  });

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
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "check @deep" } });
    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());

    fireEvent.mouseDown(screen.getByText("deep-research"));

    expect(textarea.value).toBe("check @deep-research ");
    // staged in the injected-content preview, distinct from the menu option
    expect(screen.getAllByText("deep-research").length).toBeGreaterThan(0);
  });

  it("resolves # skills against capabilities, badging unhealthy ones, and inserts the skill as text", async () => {
    const client = new FakeRouterClient();
    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        agentId="autonomous-executor"
      />,
    );
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "#security" } });

    await waitFor(() =>
      expect(menu().getByText("#security-review")).toBeTruthy(),
    );
    expect(screen.getByText("unhealthy")).toBeTruthy();

    fireEvent.mouseDown(menu().getByText("#security-review"));
    expect(textarea.value).toBe("#security-review ");
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
  });

  it("/ no longer lists skills — only commands", async () => {
    const client = new FakeRouterClient();
    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        agentId="autonomous-executor"
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    fireEvent.change(textarea, { target: { value: "/" } });
    await waitFor(() =>
      expect(menu().getByRole("option", { name: /^\/task\b/ })).toBeTruthy(),
    );
    expect(menu().queryByText(/security-review/)).toBeNull();
    expect(menu().queryByText(/verify/)).toBeNull();
  });

  describe("hotkeys", () => {
    it("Ctrl+Shift+E toggles between the textarea and the Monaco editor on linux", async () => {
      const client = new FakeRouterClient();
      render(<Composer routerClient={client} sessionId="session-active" />);
      const textarea = screen.getByPlaceholderText(/Message the agent/);

      expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
      fireEvent.keyDown(textarea, EXPAND_LINUX);
      // Monaco is lazy-loaded behind Suspense (see ExpandedComposer) — even a
      // mocked module resolves the dynamic import asynchronously, so the first
      // expand in the file must await it rather than query synchronously.
      const editor = await screen.findByTestId("fake-monaco-editor");
      expect(editor.getAttribute("data-language")).toBe("markdown");
      expect(screen.queryByPlaceholderText(/Message the agent/)).toBeNull();

      fireEvent.keyDown(editor, EXPAND_LINUX);
      expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
      expect(screen.getByPlaceholderText(/Message the agent/)).toBeTruthy();
    });

    it("plain Ctrl+E and Cmd+E do nothing on linux", async () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const textarea = screen.getByPlaceholderText(/Message the agent/);
      fireEvent.keyDown(textarea, { key: "e", ctrlKey: true });
      fireEvent.keyDown(textarea, { key: "e", metaKey: true });
      await new Promise((r) => setTimeout(r, 20));
      expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
    });

    it("Cmd+E expands on mac, and Ctrl+Shift+E does not", async () => {
      setPlatform("darwin");
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const textarea = screen.getByPlaceholderText(/Message the agent/);
      fireEvent.keyDown(textarea, EXPAND_LINUX);
      await new Promise((r) => setTimeout(r, 20));
      expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();

      fireEvent.keyDown(textarea, { key: "e", metaKey: true });
      expect(await screen.findByTestId("fake-monaco-editor")).toBeTruthy();
    });

    it("the attachFiles hotkey opens the file picker, collapsed and expanded", async () => {
      const { container } = render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const input =
        container.querySelector<HTMLInputElement>('input[type="file"]')!;
      const picked = vi.spyOn(input, "click");

      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        ATTACH_LINUX,
      );
      expect(picked).toHaveBeenCalledTimes(1);

      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        EXPAND_LINUX,
      );
      const editor = await screen.findByTestId("fake-monaco-editor");
      fireEvent.keyDown(editor, ATTACH_LINUX);
      expect(picked).toHaveBeenCalledTimes(2);
    });

    it("the toggleView hotkey switches to terminal when onModeChange is provided", () => {
      const onModeChange = vi.fn();
      const { rerender } = render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      // No handler: the chord is left alone (no throw, nothing to call).
      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        TOGGLE_VIEW_LINUX,
      );

      rerender(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
          onModeChange={onModeChange}
        />,
      );
      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        TOGGLE_VIEW_LINUX,
      );
      expect(onModeChange).toHaveBeenCalledWith("terminal");
    });
  });

  describe("+ menu", () => {
    it("lists the Input items with platform hotkey labels", () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const m = openPlusMenu();
      expect(
        m.getByRole("menuitem", { name: /Attach files…/ }).textContent,
      ).toContain("Ctrl+Shift+A");
      expect(
        m.getByRole("menuitem", { name: /Mention file or session/ }),
      ).toBeTruthy();
      expect(m.getByRole("menuitem", { name: /Commands/ })).toBeTruthy();
      expect(
        m.getByRole("menuitem", { name: /Expand editor/ }).textContent,
      ).toContain("Ctrl+Shift+E");
      expect(
        m.getByRole("menuitem", { name: /Keyboard shortcuts/ }),
      ).toBeTruthy();
    });

    it("labels hotkeys with mac glyphs on darwin", () => {
      setPlatform("darwin");
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const m = openPlusMenu();
      expect(
        m.getByRole("menuitem", { name: /Expand editor/ }).textContent,
      ).toContain("⌘E");
      expect(
        m.getByRole("menuitem", { name: /Attach files…/ }).textContent,
      ).toContain("⌘⇧A");
    });

    it("Expand editor / Collapse editor toggle the Monaco editor", async () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Expand editor/ }),
      );
      expect(await screen.findByTestId("fake-monaco-editor")).toBeTruthy();
      expect(screen.queryByRole("menu")).toBeNull(); // the menu closes on choose

      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Collapse editor/ }),
      );
      expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
      expect(screen.getByPlaceholderText(/Message the agent/)).toBeTruthy();
    });

    it("Attach files… opens the file picker", () => {
      const { container } = render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const input =
        container.querySelector<HTMLInputElement>('input[type="file"]')!;
      const picked = vi.spyOn(input, "click");
      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Attach files…/ }),
      );
      expect(picked).toHaveBeenCalledTimes(1);
    });

    it("keeps attach reachable in the expanded editor through its + menu", async () => {
      const { container } = render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        EXPAND_LINUX,
      );
      await screen.findByTestId("fake-monaco-editor");

      const input =
        container.querySelector<HTMLInputElement>('input[type="file"]')!;
      const picked = vi.spyOn(input, "click");
      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Attach files…/ }),
      );
      expect(picked).toHaveBeenCalledTimes(1);
    });

    it("Mention inserts @ at the caret and opens the mention menu", async () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;
      fireEvent.change(textarea, { target: { value: "check" } });

      fireEvent.click(
        openPlusMenu().getByRole("menuitem", {
          name: /Mention file or session/,
        }),
      );
      expect(textarea.value).toBe("check @");
      await waitFor(() =>
        expect(menu().getByText("deep-research")).toBeTruthy(),
      );
    });

    it("Commands inserts / and opens the command menu", async () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Commands/ }),
      );
      expect(textarea.value).toBe("/");
      await waitFor(() =>
        expect(menu().getByRole("option", { name: /^\/task\b/ })).toBeTruthy(),
      );
    });

    it("Mention from the expanded editor collapses back to the textarea first", async () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        EXPAND_LINUX,
      );
      await screen.findByTestId("fake-monaco-editor");

      fireEvent.click(
        openPlusMenu().getByRole("menuitem", {
          name: /Mention file or session/,
        }),
      );
      expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
      expect(
        (
          screen.getByPlaceholderText(
            /Message the agent/,
          ) as HTMLTextAreaElement
        ).value,
      ).toBe("@");
    });

    it("Keyboard shortcuts lists every binding from hotkeys.ts", () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Keyboard shortcuts/ }),
      );
      const keys = screen.getByRole("group", { name: "Keyboard shortcuts" });
      const text = keys.textContent ?? "";
      for (const label of [
        "⏎",
        "⇧⏎",
        "Ctrl+Shift+E",
        "Ctrl+⏎",
        "Esc",
        "@",
        "/",
        "Ctrl+Shift+A",
        "Ctrl+Shift+T",
      ]) {
        expect(text).toContain(label);
      }
    });

    it("hides the View section without onModeChange", () => {
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      openPlusMenu();
      expect(screen.queryByRole("group", { name: "View" })).toBeNull();
    });

    it("switches the view through the View segment", () => {
      const onModeChange = vi.fn();
      render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
          mode="chat"
          onModeChange={onModeChange}
        />,
      );
      openPlusMenu();
      const view = within(screen.getByRole("group", { name: "View" }));
      expect(
        view.getByRole("button", { name: /Chat/ }).getAttribute("aria-pressed"),
      ).toBe("true");
      fireEvent.click(view.getByRole("button", { name: /Terminal/ }));
      expect(onModeChange).toHaveBeenCalledWith("terminal");
    });

    it("hides the context row without usage, shows tokens and % with it", () => {
      const { rerender } = render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
          contextWindow={128000}
        />,
      );
      openPlusMenu();
      expect(screen.queryByTestId("composer-context")).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Input options" })); // close

      rerender(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
          usage={{ input_tokens: 106500, output_tokens: 0 }}
          contextWindow={128000}
        />,
      );
      openPlusMenu();
      expect(screen.getByTestId("composer-context").textContent).toContain(
        "Context · 106.5k / 128k (83%)",
      );
    });

    it("offers Compact now only with onCompact, and calls it", () => {
      const onCompact = vi.fn();
      const { rerender } = render(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
        />,
      );
      expect(
        openPlusMenu().queryByRole("menuitem", { name: /Compact now/ }),
      ).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Input options" })); // close

      rerender(
        <Composer
          routerClient={new FakeRouterClient()}
          sessionId="session-active"
          onCompact={onCompact}
        />,
      );
      fireEvent.click(
        openPlusMenu().getByRole("menuitem", { name: /Compact now/ }),
      );
      expect(onCompact).toHaveBeenCalledTimes(1);
    });
  });

  it("no longer renders a context meter in the input row (the dock's rim replaces it)", () => {
    render(
      <Composer
        routerClient={new FakeRouterClient()}
        sessionId="session-active"
        usage={{ input_tokens: 100000, output_tokens: 0 }}
        contextWindow={200000}
      />,
    );
    expect(screen.queryByTestId("context-meter")).toBeNull();
    expect(screen.queryByText("50%")).toBeNull();
  });

  it("renders the expanded editor with a line-number gutter and a send hint", async () => {
    render(
      <Composer
        routerClient={new FakeRouterClient()}
        sessionId="session-active"
      />,
    );
    fireEvent.keyDown(
      screen.getByPlaceholderText(/Message the agent/),
      EXPAND_LINUX,
    );
    const editor = await screen.findByTestId("fake-monaco-editor");
    expect(editor.getAttribute("data-line-numbers")).toBe("on");
    expect(screen.getByTestId("expanded-composer").textContent).toContain(
      "Ctrl+⏎ send · Esc collapse",
    );
  });

  it("round-trips typed text losslessly between collapsed and expanded", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "line one\nline two\t🎉" } });
    fireEvent.keyDown(textarea, EXPAND_LINUX);

    const editor = (await screen.findByTestId(
      "fake-monaco-editor",
    )) as HTMLTextAreaElement;
    expect(editor.value).toBe("line one\nline two\t🎉");

    fireEvent.change(editor, {
      target: { value: "line one\nline two\t🎉 edited" },
    });
    fireEvent.keyDown(editor, EXPAND_LINUX);

    expect(screen.getByPlaceholderText(/Message the agent/)).toHaveProperty(
      "value",
      "line one\nline two\t🎉 edited",
    );
  });

  it("sends from the expanded editor via Ctrl+Enter", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);
    fireEvent.keyDown(textarea, EXPAND_LINUX);

    const editor = (await screen.findByTestId(
      "fake-monaco-editor",
    )) as HTMLTextAreaElement;
    fireEvent.change(editor, { target: { value: "hello from monaco" } });
    fireEvent.keyDown(editor, { key: "Enter", ctrlKey: true });

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "hello from monaco",
        mentions: [],
        queue: true,
      }),
    );
  });

  it("shows Stop and Queue in the expanded action bar while busy", async () => {
    const onStop = vi.fn();
    render(
      <Composer
        routerClient={new FakeRouterClient()}
        sessionId="session-active"
        busy
        onStop={onStop}
      />,
    );
    fireEvent.keyDown(
      screen.getByPlaceholderText(/Message the agent/),
      EXPAND_LINUX,
    );
    await screen.findByTestId("fake-monaco-editor");
    expect(screen.getByRole("button", { name: "Queue" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /stop generating/i }));
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("Escape in the expanded editor collapses back to the textarea", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);
    fireEvent.keyDown(textarea, EXPAND_LINUX);

    const editor = await screen.findByTestId("fake-monaco-editor");
    fireEvent.keyDown(editor, { key: "Escape" });

    expect(screen.queryByTestId("fake-monaco-editor")).toBeNull();
    expect(screen.getByPlaceholderText(/Message the agent/)).toBeTruthy();
  });

  it("does not resolve @ mentions or / commands while expanded", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);
    fireEvent.keyDown(textarea, EXPAND_LINUX);

    const editor = await screen.findByTestId("fake-monaco-editor");
    fireEvent.change(editor, { target: { value: "check @deep" } });

    await new Promise((r) => setTimeout(r, 200));
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("sends the message via the Router client and clears the composer", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;
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
    const queued = {
      id: "q_1",
      text: "next question",
      queued_at: "2026-09-29T22:58:00Z",
    };
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
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;
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

  it("labels the send button Send when idle and Queue while busy", () => {
    const client = new FakeRouterClient();
    const { rerender } = render(
      <Composer routerClient={client} sessionId="session-active" />,
    );
    expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Queue" })).toBeNull();

    rerender(
      <Composer routerClient={client} sessionId="session-active" busy />,
    );
    expect(screen.getByRole("button", { name: "Queue" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Send" })).toBeNull();
  });

  it("shows a Stop button while busy and calls onStop when clicked", () => {
    const client = new FakeRouterClient();
    const onStop = vi.fn();

    const { rerender } = render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        onStop={onStop}
      />,
    );
    // Idle: no Stop button.
    expect(
      screen.queryByRole("button", { name: /stop generating/i }),
    ).toBeNull();

    rerender(
      <Composer
        routerClient={client}
        sessionId="session-active"
        busy
        onStop={onStop}
      />,
    );
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
    const { unmount } = render(
      <Composer routerClient={client} sessionId="session-active" />,
    );
    fireEvent.change(screen.getByPlaceholderText(/Message the agent/), {
      target: { value: "half-written thought" },
    });
    unmount();

    render(<Composer routerClient={client} sessionId="session-active" />);
    expect(
      (screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement)
        .value,
    ).toBe("half-written thought");
  });

  it("keeps drafts separate per session", () => {
    const client = new FakeRouterClient();
    const { unmount } = render(
      <Composer routerClient={client} sessionId="session-active" />,
    );
    fireEvent.change(screen.getByPlaceholderText(/Message the agent/), {
      target: { value: "for session A" },
    });
    unmount();

    render(<Composer routerClient={client} sessionId="session-other" />);
    expect(
      (screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement)
        .value,
    ).toBe("");
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
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;
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
    vi.spyOn(client, "sendMessage").mockRejectedValue(
      new Error("network down"),
    );
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
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    await waitFor(() =>
      expect(onSendError).toHaveBeenCalledWith("network down"),
    );
    expect(textarea.value).toBe("fix the deploy script");
  });

  it("keeps the typed text in the textarea when a manual send fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(new Error("boom"));
    const onSendError = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        onSendError={onSendError}
      />,
    );
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "hello there" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() => expect(onSendError).toHaveBeenCalledWith("boom"));
    expect(textarea.value).toBe("hello there");
  });

  it("'/agent' descends to registry agents; selecting one rebinds the session, clears the composer, and confirms — no chip, no send", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "listAgents").mockResolvedValue([
      fixtureAgent("investigator"),
    ]);
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
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/agent" } });
    await waitFor(() =>
      expect(menu().getByRole("option", { name: /^\/agent\b/ })).toBeTruthy(),
    );

    fireEvent.mouseDown(menu().getByRole("option", { name: /^\/agent\b/ }));
    expect(textarea.value).toBe("/agent ");

    await waitFor(() =>
      expect(menu().getByRole("option", { name: "investigator" })).toBeTruthy(),
    );
    fireEvent.mouseDown(menu().getByRole("option", { name: "investigator" }));

    await waitFor(() =>
      expect(onUpdateSession).toHaveBeenCalledWith({ agent: "investigator" }),
    );
    await waitFor(() =>
      expect(onCommandExecuted).toHaveBeenCalledWith(
        "Agent set to investigator",
        undefined,
      ),
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
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/model" } });
    await waitFor(() =>
      expect(menu().getByRole("option", { name: /^\/model\b/ })).toBeTruthy(),
    );

    fireEvent.mouseDown(menu().getByRole("option", { name: /^\/model\b/ }));
    expect(textarea.value).toBe("/model ");

    await waitFor(() =>
      expect(
        menu().getByRole("option", { name: "Claude Opus 5" }),
      ).toBeTruthy(),
    );
    fireEvent.mouseDown(menu().getByRole("option", { name: "Claude Opus 5" }));

    expect(client.listModels).toHaveBeenCalledWith("claudecode");
    await waitFor(() =>
      expect(onUpdateSession).toHaveBeenCalledWith({ model: "claude-opus-5" }),
    );
    await waitFor(() =>
      expect(onCommandExecuted).toHaveBeenCalledWith(
        "Model set to Claude Opus 5",
        undefined,
      ),
    );
    expect(textarea.value).toBe("");
  });

  it("a command that takes args (/task) inserts '/task ' on select for the title — stages no chip", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/task" } });
    await waitFor(() =>
      expect(menu().getByRole("option", { name: /^\/task\b/ })).toBeTruthy(),
    );

    fireEvent.mouseDown(menu().getByRole("option", { name: /^\/task\b/ }));

    expect(textarea.value).toBe("/task ");
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
  });

  it("unrecognized text after / just filters — no menu, no crash, no chip", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

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
      <Composer
        routerClient={client}
        sessionId="session-active"
        onUpdateSession={onUpdateSession}
      />,
    );
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

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
    await waitFor(() =>
      expect(menu().getByRole("option", { name: /^\/agent\b/ })).toBeTruthy(),
    );

    // descend again, then execute via Enter on the (reset) first leaf
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(textarea.value).toBe("/agent ");
    await waitFor(() =>
      expect(
        menu().getByRole("option", { name: "autonomous-executor" }),
      ).toBeTruthy(),
    );
    fireEvent.keyDown(textarea, { key: "Enter" });

    expect(textarea.value).toBe("");
    await waitFor(() =>
      expect(onUpdateSession).toHaveBeenCalledWith({
        agent: "autonomous-executor",
      }),
    );
  });

  it("typing a full '/model <id>' (no menu row matches the label) and pressing Enter runs it — nothing is sent", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "listModels").mockResolvedValue([
      { id: "claude-opus-5", name: "Claude Opus 5" },
    ]);
    const sendMessage = vi.spyOn(client, "sendMessage");
    const onUpdateSession = vi.fn();
    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        provider="claudecode"
        onUpdateSession={onUpdateSession}
      />,
    );
    const textarea = screen.getByPlaceholderText(
      /Message the agent/,
    ) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/model claude-opus-5" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() =>
      expect(onUpdateSession).toHaveBeenCalledWith({ model: "claude-opus-5" }),
    );
    expect(sendMessage).not.toHaveBeenCalled();
  });

  describe("command registry", () => {
    it("renders an unavailable command disabled with its reason; arrows skip it and Enter on it only shows the reason", async () => {
      const client = new FakeRouterClient();
      const sendMessage = vi.spyOn(client, "sendMessage");
      const ran = vi.fn();
      const commands: CommandDescriptor[] = [
        {
          name: "new-leader",
          description: "start a session that leads this one",
          available: () => ({
            ok: false,
            reason: "already led by triage-lead",
          }),
          execute: ran,
        },
        { name: "new", description: "start a session", execute: ran },
      ];
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, { target: { value: "/new" } });
      const row = await waitFor(() =>
        menu().getByRole("option", { name: /^\/new-leader/ }),
      );
      expect(row.getAttribute("aria-disabled")).toBe("true");
      expect(within(row).getByText("already led by triage-lead")).toBeTruthy();

      // Exact "/new" is ranked first and active; arrowing never lands on the disabled row.
      const active = () => menu().getByRole("option", { selected: true });
      expect(active().textContent).toMatch(/^\/new(?!-)/);
      fireEvent.keyDown(textarea, { key: "ArrowDown" });
      expect(active().textContent).toMatch(/^\/new(?!-)/);

      // Clicking it does nothing.
      fireEvent.mouseDown(row);
      expect(ran).not.toHaveBeenCalled();

      // Typed in full + Enter: the reason, inline; nothing runs or sends, the text stays.
      fireEvent.change(textarea, { target: { value: "/new-leader" } });
      await waitFor(() =>
        expect(menu().getAllByRole("option")).toHaveLength(1),
      );
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(screen.getByRole("alert").textContent).toBe(
        "already led by triage-lead",
      );
      expect(textarea.value).toBe("/new-leader");

      fireEvent.change(textarea, {
        target: { value: "/new-leader with a goal" },
      });
      expect(screen.queryByRole("alert")).toBeNull(); // an edit clears the notice
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(screen.getByRole("alert").textContent).toBe(
        "already led by triage-lead",
      );

      expect(ran).not.toHaveBeenCalled();
      expect(sendMessage).not.toHaveBeenCalled();
    });

    it("Enter runs an optional-args command at once; Tab completes '/name ' for args, then Enter runs with them", async () => {
      const client = new FakeRouterClient();
      const ran = vi.fn();
      const commands: CommandDescriptor[] = [
        {
          name: "compact",
          description: "Compact the conversation",
          argHint: "[focus]",
          execute: ran,
        },
        {
          name: "task",
          description: "Create a task",
          argHint: "<title>",
          argRequired: "/task needs a title",
          execute: ran,
        },
      ];
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, { target: { value: "/compact" } });
      await waitFor(() =>
        menu().getByRole("option", { name: /^\/compact/ }),
      );
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(ran).toHaveBeenCalledWith({ args: "" }, expect.anything());
      expect(textarea.value).toBe("");

      fireEvent.change(textarea, { target: { value: "/c" } });
      await waitFor(() =>
        menu().getByRole("option", { name: /^\/compact/ }),
      );
      fireEvent.keyDown(textarea, { key: "Tab" });
      expect(textarea.value).toBe("/compact ");
      expect(ran).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());

      fireEvent.change(textarea, { target: { value: "/compact keep the API notes" } });
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(ran).toHaveBeenLastCalledWith(
        { args: "keep the API notes" },
        expect.anything(),
      );

      // A required arg still inserts on Enter rather than refusing.
      fireEvent.change(textarea, { target: { value: "/task" } });
      await waitFor(() => menu().getByRole("option", { name: /^\/task/ }));
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(textarea.value).toBe("/task ");
      expect(ran).toHaveBeenCalledTimes(2);
    });

    it("a runsWhenUnavailable command stays disabled in the menu but typed + Enter runs it", async () => {
      const client = new FakeRouterClient();
      const ran = vi.fn();
      const commands: CommandDescriptor[] = [
        {
          name: "new-leader",
          description: "start a session that leads this one",
          argHint: "[goal]",
          available: () => ({ ok: false, reason: "already led by triage-lead" }),
          runsWhenUnavailable: true,
          execute: ran,
        },
      ];
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, { target: { value: "/new-leader" } });
      const row = await waitFor(() =>
        menu().getByRole("option", { name: /^\/new-leader/ }),
      );
      fireEvent.mouseDown(row);
      expect(ran).not.toHaveBeenCalled();
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(ran).toHaveBeenCalledWith({ args: "" }, expect.anything());

      fireEvent.change(textarea, { target: { value: "/new-leader a goal" } });
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(ran).toHaveBeenLastCalledWith({ args: "a goal" }, expect.anything());
    });

    it("an alias matches in the menu and runs its command when typed + Enter", async () => {
      const client = new FakeRouterClient();
      const execute = vi.fn();
      const commands: CommandDescriptor[] = [
        {
          name: "usage",
          aliases: ["status", "context"],
          description: "usage and health",
          execute,
        },
      ];
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, { target: { value: "/stat" } });
      const row = await waitFor(() =>
        menu().getByRole("option", { name: /^\/usage/ }),
      );
      expect(within(row).getByText("/status")).toBeTruthy();

      fireEvent.change(textarea, { target: { value: "/status" } });
      await waitFor(() =>
        expect(menu().getByRole("option", { name: /^\/usage/ })).toBeTruthy(),
      );
      fireEvent.keyDown(textarea, { key: "Enter" });

      expect(execute).toHaveBeenCalledWith({ args: "" }, expect.anything());
      expect(textarea.value).toBe("");
    });

    it("a session record update while the menu is open keeps the arrowed selection", async () => {
      const client = new FakeRouterClient();
      const noop = vi.fn();
      const commands: CommandDescriptor[] = ["alpha", "beta", "gamma"].map(
        (name) => ({ name, description: name, execute: noop }),
      );
      const session: Session = {
        id: "session-active",
        name: "dns-tracing",
        activity: "active",
        created_at: "2026-10-01T00:00:00Z",
        updated_at: "2026-10-01T00:00:00Z",
      };
      const { rerender } = render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          session={session}
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;
      fireEvent.change(textarea, { target: { value: "/" } });
      await waitFor(() => expect(menu().getAllByRole("option")).toHaveLength(3));
      fireEvent.keyDown(textarea, { key: "ArrowDown" });
      fireEvent.keyDown(textarea, { key: "ArrowDown" });
      const active = () => menu().getByRole("option", { selected: true });
      expect(active().textContent).toMatch(/^\/gamma/);

      // A turn ends: a new record, nothing a command reads changed.
      rerender(
        <Composer
          routerClient={client}
          sessionId="session-active"
          session={{ ...session, activity: "inactive", updated_at: "2026-10-01T00:01:00Z" }}
          commands={commands}
        />,
      );
      await new Promise((r) => setTimeout(r, 200)); // past the resolver's debounce
      expect(active().textContent).toMatch(/^\/gamma/);
    });

    it("Enter acts on what's typed, not on rows resolved for an earlier query", async () => {
      const client = new FakeRouterClient();
      const compact = vi.fn();
      const usage = vi.fn();
      const commands: CommandDescriptor[] = [
        { name: "compact", description: "compact", execute: compact },
        {
          name: "usage",
          aliases: ["context"],
          description: "usage",
          execute: usage,
        },
      ];
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      // "/c", a pause (the menu resolves with /compact first)…
      fireEvent.change(textarea, { target: { value: "/c" } });
      await waitFor(() =>
        expect(
          menu().getByRole("option", { selected: true }).textContent,
        ).toMatch(/^\/compact/),
      );
      // …then "ontext" + Enter inside the debounce.
      fireEvent.change(textarea, { target: { value: "/context" } });
      fireEvent.keyDown(textarea, { key: "Enter" });

      expect(compact).not.toHaveBeenCalled();
      expect(usage).toHaveBeenCalledWith({ args: "" }, expect.anything());
      expect(textarea.value).toBe("");
    });

    it("Enter with stale rows and no exact command waits for the rows", async () => {
      const client = new FakeRouterClient();
      const compact = vi.fn();
      const commit = vi.fn();
      const commands: CommandDescriptor[] = [
        { name: "compact", description: "compact", execute: compact },
        { name: "commit", description: "commit", execute: commit },
      ];
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={commands}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;
      fireEvent.change(textarea, { target: { value: "/c" } });
      await waitFor(() => expect(menu().getAllByRole("option")).toHaveLength(2));
      fireEvent.change(textarea, { target: { value: "/commi" } });
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(compact).not.toHaveBeenCalled();
      expect(commit).not.toHaveBeenCalled();
      expect(textarea.value).toBe("/commi");

      await waitFor(() => expect(menu().getAllByRole("option")).toHaveLength(1));
      fireEvent.keyDown(textarea, { key: "Enter" });
      expect(commit).toHaveBeenCalledOnce();
      expect(compact).not.toHaveBeenCalled();
    });

    it("sends an unknown /word to the agent as plain text", async () => {
      const client = new FakeRouterClient();
      const sendMessage = vi.spyOn(client, "sendMessage");
      render(<Composer routerClient={client} sessionId="session-active" />);
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, {
        target: { value: "/clear the context please" },
      });
      fireEvent.keyDown(textarea, { key: "Enter" });

      await waitFor(() =>
        expect(sendMessage).toHaveBeenCalledWith(
          "session-active",
          expect.objectContaining({ text: "/clear the context please" }),
        ),
      );
    });
  });

  describe("command card host", () => {
    function cardCommand(): CommandDescriptor {
      return {
        name: "usage",
        description: "usage and health",
        execute: (_inv, api) =>
          api.openCard((close) => (
            <div>
              <span>Context 71%</span>
              <button type="button" onClick={close}>
                Done
              </button>
            </div>
          )),
      };
    }

    async function openCard(client = new FakeRouterClient()) {
      const sendMessage = vi.spyOn(client, "sendMessage");
      const onSent = vi.fn();
      render(
        <div>
          <p>elsewhere</p>
          <Composer
            routerClient={client}
            sessionId="session-active"
            commands={[cardCommand()]}
            onSent={onSent}
          />
        </div>,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;
      fireEvent.change(textarea, { target: { value: "/usage" } });
      await waitFor(() =>
        expect(menu().getByRole("option", { name: /^\/usage/ })).toBeTruthy(),
      );
      fireEvent.mouseDown(menu().getByRole("option", { name: /^\/usage/ }));
      expect(screen.getByTestId("command-card").textContent).toContain(
        "Context 71%",
      );
      return { textarea, sendMessage, onSent };
    }

    it("opens a command's card above the composer, never in the transcript", async () => {
      const { textarea, sendMessage, onSent } = await openCard();
      expect(textarea.value).toBe("");
      expect(sendMessage).not.toHaveBeenCalled();
      expect(onSent).not.toHaveBeenCalled();
    });

    it("Esc in the composer dismisses it and keeps focus there", async () => {
      const { textarea } = await openCard();
      fireEvent.keyDown(textarea, { key: "Escape" });
      expect(screen.queryByTestId("command-card")).toBeNull();
      await waitFor(() => expect(document.activeElement).toBe(textarea));
    });

    it("Esc inside the card dismisses it and returns focus to the composer", async () => {
      const { textarea } = await openCard();
      const done = screen.getByRole("button", { name: "Done" });
      done.focus();
      fireEvent.keyDown(done, { key: "Escape" });
      expect(screen.queryByTestId("command-card")).toBeNull();
      await waitFor(() => expect(document.activeElement).toBe(textarea));
    });

    it("Esc in a menu inside the card closes only the menu; the next Esc closes the card", async () => {
      function Pill() {
        const [open, setOpen] = useState(false);
        return (
          <Popover
            open={open}
            onClose={() => setOpen(false)}
            anchor={
              <button type="button" onClick={() => setOpen((o) => !o)}>
                trust
              </button>
            }
          >
            <button type="button">Auto review</button>
          </Popover>
        );
      }
      const client = new FakeRouterClient();
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          commands={[
            {
              name: "new",
              description: "start a session",
              execute: (_inv, api) => api.openCard(() => <Pill />),
            },
          ]}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;
      fireEvent.change(textarea, { target: { value: "/new" } });
      fireEvent.mouseDown(
        await waitFor(() => menu().getByRole("option", { name: /^\/new/ })),
      );
      fireEvent.click(screen.getByRole("button", { name: "trust" }));
      const item = screen.getByRole("button", { name: "Auto review" });
      item.focus();

      fireEvent.keyDown(item, { key: "Escape" });
      expect(screen.queryByRole("button", { name: "Auto review" })).toBeNull();
      expect(screen.getByTestId("command-card")).toBeTruthy();

      const trust = screen.getByRole("button", { name: "trust" });
      trust.focus();
      fireEvent.keyDown(trust, { key: "Escape" });
      expect(screen.queryByTestId("command-card")).toBeNull();
      await waitFor(() => expect(document.activeElement).toBe(textarea));
    });

    it("a click away dismisses it; a click inside doesn't", async () => {
      const { textarea } = await openCard();
      await new Promise((r) => setTimeout(r, 0)); // the host arms click-away a tick after opening
      fireEvent.mouseDown(screen.getByText("Context 71%"));
      expect(screen.getByTestId("command-card")).toBeTruthy();
      fireEvent.mouseDown(screen.getByText("elsewhere"));
      expect(screen.queryByTestId("command-card")).toBeNull();
      await waitFor(() => expect(document.activeElement).toBe(textarea));
    });

    it("the card's close callback dismisses it", async () => {
      await openCard();
      fireEvent.click(screen.getByRole("button", { name: "Done" }));
      expect(screen.queryByTestId("command-card")).toBeNull();
    });
  });

  describe("/task", () => {
    it("creates a task with the typed title bound to this session, confirms with an Open task link, sends nothing", async () => {
      const client = new FakeRouterClient();
      const createTask = vi.spyOn(client, "createTask");
      const sendMessage = vi.spyOn(client, "sendMessage");
      const onCommandExecuted = vi.fn();
      const onOpenTask = vi.fn();
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          onCommandExecuted={onCommandExecuted}
          onOpenTask={onOpenTask}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, {
        target: { value: "/task reproduce DNS error for tracing service" },
      });
      fireEvent.keyDown(textarea, { key: "Enter" });

      expect(textarea.value).toBe("");
      await waitFor(() => expect(onCommandExecuted).toHaveBeenCalled());
      expect(createTask).toHaveBeenCalledWith({
        title: "reproduce DNS error for tracing service",
        session_id: "session-active",
        source: "user",
      });
      const created = await createTask.mock.results[0].value;
      const [message, action] = onCommandExecuted.mock.calls[0];
      expect(message).toBe(
        "Task created: reproduce DNS error for tracing service",
      );
      expect(action.label).toBe("Open task");
      action.onClick();
      expect(onOpenTask).toHaveBeenCalledWith(created.id);
      expect(sendMessage).not.toHaveBeenCalled();
    });

    it("with no title shows an inline 'needs a title' and creates nothing", async () => {
      const client = new FakeRouterClient();
      const createTask = vi.spyOn(client, "createTask");
      const sendMessage = vi.spyOn(client, "sendMessage");
      render(<Composer routerClient={client} sessionId="session-active" />);
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, { target: { value: "/task " } });
      fireEvent.keyDown(textarea, { key: "Enter" });

      expect(screen.getByRole("alert").textContent).toBe("/task needs a title");
      expect(textarea.value).toBe("/task ");
      await new Promise((r) => setTimeout(r, 20));
      expect(createTask).not.toHaveBeenCalled();
      expect(sendMessage).not.toHaveBeenCalled();
    });

    it("surfaces a Router failure through onCommandError", async () => {
      const client = new FakeRouterClient();
      vi.spyOn(client, "createTask").mockRejectedValue(
        new Error("router down"),
      );
      const onCommandError = vi.fn();
      render(
        <Composer
          routerClient={client}
          sessionId="session-active"
          onCommandError={onCommandError}
        />,
      );
      const textarea = screen.getByPlaceholderText(
        /Message the agent/,
      ) as HTMLTextAreaElement;

      fireEvent.change(textarea, { target: { value: "/task flaky" } });
      fireEvent.keyDown(textarea, { key: "Enter" });

      await waitFor(() =>
        expect(onCommandError).toHaveBeenCalledWith("router down"),
      );
    });
  });

  describe("attachments", () => {
    function pasteFiles(target: Element, files: File[]) {
      fireEvent.paste(target, { clipboardData: { files, types: ["Files"] } });
    }

    it("uploads a pasted screenshot to the session under a dated name and shows its thumbnail", async () => {
      const client = new FakeRouterClient();
      render(<Composer routerClient={client} sessionId="s1" />);
      pasteFiles(screen.getByPlaceholderText(/Message the agent/), [
        new File([new Uint8Array(8)], "image.png", { type: "image/png" }),
      ]);

      await waitFor(() => expect(client.uploadedAttachments).toHaveLength(1));
      const { owner, attachment } = client.uploadedAttachments[0];
      expect(owner).toEqual({ session: "s1" });
      expect(attachment.name).toMatch(
        /^screenshot-\d{4}-\d{2}-\d{2}-\d{6}\.png$/,
      );
      const chip = (await screen.findByLabelText(`Remove ${attachment.name}`))
        .parentElement!;
      await waitFor(() =>
        expect(chip.querySelector("img")?.getAttribute("src")).toBe(
          `fixture://sessions/s1/attachments/${attachment.id}/thumbnail`,
        ),
      );
    });

    it("stages a file pasted into the expanded editor", async () => {
      const client = new FakeRouterClient();
      render(<Composer routerClient={client} sessionId="s1" />);
      fireEvent.keyDown(
        screen.getByPlaceholderText(/Message the agent/),
        EXPAND_LINUX,
      );
      const editor = await screen.findByTestId("fake-monaco-editor");
      pasteFiles(editor, [
        new File(["x"], "notes.txt", { type: "text/plain" }),
      ]);
      await waitFor(() =>
        expect(client.uploadedAttachments[0]?.attachment.name).toBe(
          "notes.txt",
        ),
      );
    });

    it("leaves a plain-text paste to the textarea", () => {
      const client = new FakeRouterClient();
      render(<Composer routerClient={client} sessionId="s1" />);
      const textarea = screen.getByPlaceholderText(/Message the agent/);
      const notPrevented = fireEvent.paste(textarea, {
        clipboardData: { files: [], types: ["text/plain"] },
      });
      expect(notPrevented).toBe(true);
      expect(client.uploadedAttachments).toHaveLength(0);
    });

    it("stages dropped files", async () => {
      const client = new FakeRouterClient();
      const { container } = render(
        <Composer routerClient={client} sessionId="s1" />,
      );
      const file = new File(["log"], "build.log", { type: "text/plain" });
      fireEvent.drop(container.firstElementChild!, {
        dataTransfer: { files: [file], types: ["Files"] },
      });
      await waitFor(() =>
        expect(client.uploadedAttachments[0]?.attachment.name).toBe(
          "build.log",
        ),
      );
    });

    it("sends staged attachments by id, shows them in the sent bubble's text, and clears them", async () => {
      const client = new FakeRouterClient();
      const send = vi.spyOn(client, "sendMessage");
      const onSent = vi.fn();
      render(<Composer routerClient={client} sessionId="s1" onSent={onSent} />);
      const textarea = screen.getByPlaceholderText(/Message the agent/);
      pasteFiles(textarea, [
        new File(["x"], "notes.txt", { type: "text/plain" }),
      ]);
      await screen.findByText("1 B");

      fireEvent.change(textarea, { target: { value: "read this" } });
      fireEvent.keyDown(textarea, { key: "Enter" });

      await waitFor(() => expect(onSent).toHaveBeenCalled());
      const { attachment } = client.uploadedAttachments[0];
      expect(send).toHaveBeenCalledWith(
        "s1",
        expect.objectContaining({
          text: "read this",
          attachments: [attachment.id],
        }),
      );
      expect(onSent.mock.calls[0][0]).toContain(`- ${attachment.path}`);
      expect(screen.queryByLabelText("Remove notes.txt")).toBeNull();
      // Sent files belong to the message now — nothing was deleted.
      expect(client.deletedAttachments).toEqual([]);
    });

    it("can send attachments with no text", async () => {
      const client = new FakeRouterClient();
      const send = vi.spyOn(client, "sendMessage");
      render(<Composer routerClient={client} sessionId="s1" />);
      pasteFiles(screen.getByPlaceholderText(/Message the agent/), [
        new File(["x"], "a.txt"),
      ]);
      await screen.findByText("1 B");
      fireEvent.click(screen.getByText("Send"));
      await waitFor(() =>
        expect(send).toHaveBeenCalledWith(
          "s1",
          expect.objectContaining({ text: "", attachments: ["att_1"] }),
        ),
      );
    });

    it("holds Send until uploads land", async () => {
      const client = new FakeRouterClient();
      let land: () => void = () => {};
      vi.spyOn(client, "uploadAttachment").mockImplementation(
        (_owner, file, name) =>
          new Promise((resolve) => {
            land = () =>
              resolve({
                id: "att_slow",
                name,
                path: `/a/s1/att_slow/${name}`,
                size: file.size,
                thumbnail: false,
              });
          }),
      );
      render(<Composer routerClient={client} sessionId="s1" />);
      const textarea = screen.getByPlaceholderText(/Message the agent/);
      fireEvent.change(textarea, { target: { value: "hi" } });
      pasteFiles(textarea, [new File(["x"], "a.txt")]);

      const send = screen.getByText("Send").closest("button")!;
      await waitFor(() => expect(send.disabled).toBe(true));
      expect(screen.getByText("Uploading…")).toBeTruthy();
      land();
      await waitFor(() => expect(send.disabled).toBe(false));
    });

    it("removing a staged file deletes it from the Router", async () => {
      const client = new FakeRouterClient();
      render(<Composer routerClient={client} sessionId="s1" />);
      pasteFiles(screen.getByPlaceholderText(/Message the agent/), [
        new File(["x"], "a.txt"),
      ]);
      fireEvent.click(await screen.findByLabelText("Remove a.txt"));
      await waitFor(() => expect(client.deletedAttachments).toEqual(["att_1"]));
    });

    it("keeps staged files across a remount, like the text draft", async () => {
      const client = new FakeRouterClient();
      const { unmount } = render(
        <Composer routerClient={client} sessionId="s1" />,
      );
      pasteFiles(screen.getByPlaceholderText(/Message the agent/), [
        new File(["x"], "a.txt"),
      ]);
      await screen.findByLabelText("Remove a.txt");
      unmount();
      render(<Composer routerClient={client} sessionId="s1" />);
      expect(screen.getByLabelText("Remove a.txt")).toBeTruthy();
    });
  });
});
