import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ExpandedComposer } from "./ExpandedComposer";
import { monacoKeybinding } from "./monacoKeybinding";
import { setPlatform } from "../../platform/shortcut";

const CTRL_CMD = 1 << 11;
const SHIFT = 1 << 10;
const KEY_ENTER = 3;
const KEY_ESCAPE = 9;
const KEY_E = 35;

// Same fake as Composer.test.tsx: records addCommand keybindings and fires
// them from the stand-in textarea, so the real onMount wiring is exercised.
const registered: number[] = [];
vi.mock("@monaco-editor/react", () => ({
  Editor: (props: {
    value: string;
    options?: { lineNumbers?: string; wordWrap?: string; minimap?: { enabled?: boolean } };
    onChange?: (value: string) => void;
    onMount?: (editor: unknown, monaco: unknown) => void;
  }) => {
    const commands = new Map<number, () => void>();
    const fakeEditor = {
      addCommand: (keybinding: number, handler: () => void) => {
        commands.set(keybinding, handler);
        registered.push(keybinding);
      },
      focus: () => {},
      setPosition: () => {},
      revealPositionInCenterIfOutsideViewport: () => {},
      getModel: () => ({
        getPositionAt: (offset: number) => ({ lineNumber: 1, column: offset + 1 }),
        getOffsetAt: (pos: { column: number }) => pos.column - 1,
      }),
      onDidChangeCursorPosition: () => ({ dispose: () => {} }),
    };
    props.onMount?.(fakeEditor, {
      KeyMod: { CtrlCmd: CTRL_CMD, Shift: SHIFT },
      KeyCode: { Enter: KEY_ENTER, Escape: KEY_ESCAPE },
    });
    return (
      <textarea
        data-testid="fake-monaco-editor"
        data-line-numbers={props.options?.lineNumbers}
        data-word-wrap={props.options?.wordWrap}
        data-minimap={String(props.options?.minimap?.enabled)}
        value={props.value}
        onChange={(e) => props.onChange?.(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") commands.get(CTRL_CMD | KEY_ENTER)?.();
          if (e.key === "Escape") commands.get(KEY_ESCAPE)?.();
        }}
      />
    );
  },
}));

describe("ExpandedComposer", () => {
  beforeEach(() => {
    registered.length = 0;
    setPlatform("linux");
  });
  afterEach(() => setPlatform(undefined));

  it("shows a line-number gutter with word wrap on and no minimap", async () => {
    render(<ExpandedComposer value="" onChange={() => {}} onSend={() => {}} onCollapse={() => {}} />);
    const editor = await screen.findByTestId("fake-monaco-editor");
    expect(editor.getAttribute("data-line-numbers")).toBe("on");
    expect(editor.getAttribute("data-word-wrap")).toBe("on");
    expect(editor.getAttribute("data-minimap")).toBe("false");
  });

  it("binds the sendFromEditor hotkey (CtrlCmd+Enter) and Escape collapse", async () => {
    const onSend = vi.fn();
    const onCollapse = vi.fn();
    render(<ExpandedComposer value="draft" onChange={() => {}} onSend={onSend} onCollapse={onCollapse} />);
    const editor = await screen.findByTestId("fake-monaco-editor");
    expect(registered).toContain(CTRL_CMD | KEY_ENTER);

    fireEvent.keyDown(editor, { key: "Enter", ctrlKey: true });
    expect(onSend).toHaveBeenCalledWith("draft");
    fireEvent.keyDown(editor, { key: "Escape" });
    expect(onCollapse).toHaveBeenCalledTimes(1);
  });

  it("renders the action bar: leading slot, hint, Stop and Queue while busy", async () => {
    const onStop = vi.fn();
    render(
      <ExpandedComposer
        value=""
        onChange={() => {}}
        onSend={() => {}}
        onCollapse={() => {}}
        busy
        onStop={onStop}
        leading={<button type="button">plus</button>}
      />,
    );
    await screen.findByTestId("fake-monaco-editor");
    expect(screen.getByRole("button", { name: "plus" })).toBeTruthy();
    expect(screen.getByTestId("expanded-composer").textContent).toContain("Ctrl+⏎ send · Esc collapse");
    expect(screen.getByRole("button", { name: "Queue" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /stop generating/i }));
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("uses mac labels on darwin", async () => {
    setPlatform("darwin");
    render(<ExpandedComposer value="" onChange={() => {}} onSend={() => {}} onCollapse={() => {}} />);
    await screen.findByTestId("fake-monaco-editor");
    expect(screen.getByTestId("expanded-composer").textContent).toContain("⌘⏎ send · Esc collapse");
  });

  it("serves terminal compose: custom send label, no Stop/Queue, sends the text", async () => {
    const onSend = vi.fn();
    render(
      <ExpandedComposer
        value="ls -la"
        onChange={() => {}}
        onSend={onSend}
        onCollapse={() => {}}
        sendLabel="Paste into terminal"
        showBusyControls={false}
        busy
        onStop={() => {}}
        hintSuffix="sent as a bracketed paste"
      />,
    );
    await screen.findByTestId("fake-monaco-editor");
    expect(screen.queryByRole("button", { name: "Queue" })).toBeNull();
    expect(screen.queryByRole("button", { name: /stop generating/i })).toBeNull();
    expect(screen.getByTestId("expanded-composer").textContent).toContain("sent as a bracketed paste");
    fireEvent.click(screen.getByRole("button", { name: "Paste into terminal" }));
    expect(onSend).toHaveBeenCalledWith("ls -la");
  });

  it("disables send with the blocked reason as its tooltip", async () => {
    render(
      <ExpandedComposer
        value="hi"
        onChange={() => {}}
        onSend={() => {}}
        onCollapse={() => {}}
        sendBlockedReason="Waiting for attachments to finish uploading"
      />,
    );
    await screen.findByTestId("fake-monaco-editor");
    const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    expect(send.disabled).toBe(true);
    expect(send.title).toBe("Waiting for attachments to finish uploading");
  });
});

describe("monacoKeybinding", () => {
  const monaco = {
    KeyMod: { CtrlCmd: CTRL_CMD, Shift: SHIFT },
    KeyCode: { Enter: KEY_ENTER, Escape: KEY_ESCAPE, KeyE: KEY_E },
  } as unknown as Parameters<typeof monacoKeybinding>[1];

  it("maps mod, shift and key", () => {
    expect(monacoKeybinding({ key: "Enter", mod: true, shift: false }, monaco)).toBe(CTRL_CMD | KEY_ENTER);
    expect(monacoKeybinding({ key: "e", mod: true, shift: true }, monaco)).toBe(CTRL_CMD | SHIFT | KEY_E);
    expect(monacoKeybinding({ key: "Escape", mod: false, shift: false }, monaco)).toBe(KEY_ESCAPE);
  });

  it("returns undefined for a key it can't map", () => {
    expect(monacoKeybinding({ key: "F13", mod: false, shift: false }, monaco)).toBeUndefined();
  });
});
