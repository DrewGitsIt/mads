import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TerminalComposer, formatBracketedPaste } from "./TerminalComposer";

// Same fake-Monaco precedent as Composer.test.tsx — drives the real onMount
// callback (addCommand for ⌘Enter send) so that keybinding is exercised the
// same way the real editor's keybinding service would. Escape is deliberately
// NOT registered via addCommand here — TerminalComposer handles it via a
// single capture-phase listener on the overlay instead (see TerminalComposer.tsx),
// so the fake's own onKeyDown never needs to simulate an Escape command.
const CTRL_CMD = 1 << 11;
const KEY_ENTER = 3;

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
    };
    const monaco = { KeyMod: { CtrlCmd: CTRL_CMD }, KeyCode: { Enter: KEY_ENTER } };
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
        }}
      />
    );
  },
}));

describe("TerminalComposer", () => {
  it("renders nothing when closed", () => {
    render(<TerminalComposer open={false} onClose={vi.fn()} onSend={vi.fn()} />);
    expect(screen.queryByTestId("terminal-composer")).toBeNull();
  });

  it("⌘E-triggered open shows the Monaco editor", async () => {
    render(<TerminalComposer open onClose={vi.fn()} onSend={vi.fn()} />);
    // Monaco is lazy-loaded behind Suspense — even a mocked module resolves
    // the dynamic import asynchronously, so the first render in the file
    // must await it rather than query synchronously.
    const editor = await screen.findByTestId("fake-monaco-editor");
    expect(editor.getAttribute("data-language")).toBe("markdown");
  });

  it("closes exactly once on Escape inside the editor", async () => {
    const onClose = vi.fn();
    render(<TerminalComposer open onClose={onClose} onSend={vi.fn()} />);

    fireEvent.keyDown(await screen.findByTestId("fake-monaco-editor"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on the Cancel button", () => {
    const onClose = vi.fn();
    render(<TerminalComposer open onClose={onClose} onSend={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("sends the exact typed text via ⌘Enter, not a bracketed-paste-formatted version — the caller owns formatting", async () => {
    const onSend = vi.fn();
    render(<TerminalComposer open onClose={vi.fn()} onSend={onSend} />);

    const editor = await screen.findByTestId("fake-monaco-editor");
    fireEvent.change(editor, { target: { value: "line one\nline two" } });
    fireEvent.keyDown(editor, { key: "Enter", metaKey: true });

    expect(onSend).toHaveBeenCalledWith("line one\nline two");
  });

  it("sends via the Send button too", async () => {
    const onSend = vi.fn();
    render(<TerminalComposer open onClose={vi.fn()} onSend={onSend} />);

    fireEvent.change(await screen.findByTestId("fake-monaco-editor"), {
      target: { value: "hello pty" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(onSend).toHaveBeenCalledWith("hello pty");
  });

  it("does not resolve @ mentions or / commands — no menu ever renders", async () => {
    render(<TerminalComposer open onClose={vi.fn()} onSend={vi.fn()} />);

    fireEvent.change(await screen.findByTestId("fake-monaco-editor"), {
      target: { value: "check @deep /agent" },
    });
    await new Promise((r) => setTimeout(r, 200));

    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("clears its text between opens", async () => {
    const onSend = vi.fn();
    const { rerender } = render(<TerminalComposer open onClose={vi.fn()} onSend={onSend} />);

    fireEvent.change(await screen.findByTestId("fake-monaco-editor"), {
      target: { value: "leftover text" },
    });
    rerender(<TerminalComposer open={false} onClose={vi.fn()} onSend={onSend} />);
    rerender(<TerminalComposer open onClose={vi.fn()} onSend={onSend} />);

    expect(await screen.findByTestId("fake-monaco-editor")).toHaveProperty("value", "");
  });
});

describe("formatBracketedPaste", () => {
  it("wraps text in the standard xterm bracketed-paste escape sequence", () => {
    expect(formatBracketedPaste("hello")).toBe("\x1b[200~hello\x1b[201~");
  });

  it("preserves multi-line and special-character content verbatim", () => {
    const text = "line one\nline two\t🎉";
    expect(formatBracketedPaste(text)).toBe(`\x1b[200~${text}\x1b[201~`);
  });
});
