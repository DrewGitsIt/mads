import { afterEach, describe, expect, it } from "vitest";
import { hotkeyLabel, matches, setHotkeyOverrides } from "./hotkeys";

const ev = (o: Partial<KeyboardEvent> & { key: string }) => ({
  metaKey: false,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  ...o,
});

afterEach(() => setHotkeyOverrides({}));

describe("hotkeys on mac", () => {
  it("expand is Cmd+E exactly", () => {
    expect(matches("expandEditor", ev({ key: "e", metaKey: true }), undefined, "darwin")).toBe(true);
    expect(matches("expandEditor", ev({ key: "e", ctrlKey: true }), undefined, "darwin")).toBe(false);
    expect(matches("expandEditor", ev({ key: "e", metaKey: true, shiftKey: true }), undefined, "darwin")).toBe(false);
    expect(matches("expandEditor", ev({ key: "e", metaKey: true, altKey: true }), undefined, "darwin")).toBe(false);
  });

  it("view toggle and attach need Cmd+Shift", () => {
    const t = ev({ key: "T", metaKey: true, shiftKey: true });
    expect(matches("toggleView", t, undefined, "darwin")).toBe(true);
    expect(matches("attachFiles", ev({ key: "A", metaKey: true, shiftKey: true }), undefined, "darwin")).toBe(true);
    expect(matches("toggleView", ev({ key: "t", metaKey: true }), undefined, "darwin")).toBe(false);
  });

  it("find is Cmd+F; next and previous are Cmd+G and Cmd+Shift+G", () => {
    expect(matches("find", ev({ key: "f", metaKey: true }), undefined, "darwin")).toBe(true);
    expect(matches("find", ev({ key: "f", ctrlKey: true }), undefined, "darwin")).toBe(false);
    expect(matches("findNext", ev({ key: "g", metaKey: true }), undefined, "darwin")).toBe(true);
    expect(matches("findPrevious", ev({ key: "G", metaKey: true, shiftKey: true }), undefined, "darwin")).toBe(true);
    expect(matches("findNext", ev({ key: "G", metaKey: true, shiftKey: true }), undefined, "darwin")).toBe(false);
  });

  it("labels use glyphs", () => {
    expect(hotkeyLabel("expandEditor", undefined, "darwin")).toBe("⌘E");
    expect(hotkeyLabel("sendFromEditor", undefined, "darwin")).toBe("⌘⏎");
    expect(hotkeyLabel("toggleView", undefined, "darwin")).toBe("⌘⇧T");
    expect(hotkeyLabel("collapseEditor", undefined, "darwin")).toBe("Esc");
  });
});

describe("hotkeys on linux", () => {
  it("plain Ctrl+E does NOT match expand; Ctrl+Shift+E does", () => {
    expect(matches("expandEditor", ev({ key: "e", ctrlKey: true }), undefined, "linux")).toBe(false);
    expect(matches("expandEditor", ev({ key: "E", ctrlKey: true, shiftKey: true }), undefined, "linux")).toBe(true);
    expect(matches("expandEditor", ev({ key: "E", metaKey: true, shiftKey: true }), undefined, "linux")).toBe(false);
    expect(matches("expandEditor", ev({ key: "E", ctrlKey: true, shiftKey: true, altKey: true }), undefined, "linux")).toBe(false);
  });

  it("send is Ctrl+Enter exactly; collapse is bare Esc", () => {
    expect(matches("sendFromEditor", ev({ key: "Enter", ctrlKey: true }), undefined, "linux")).toBe(true);
    expect(matches("sendFromEditor", ev({ key: "Enter", ctrlKey: true, shiftKey: true }), undefined, "linux")).toBe(false);
    expect(matches("sendFromEditor", ev({ key: "Enter" }), undefined, "linux")).toBe(false);
    expect(matches("collapseEditor", ev({ key: "Escape" }), undefined, "linux")).toBe(true);
    expect(matches("collapseEditor", ev({ key: "Escape", ctrlKey: true }), undefined, "linux")).toBe(false);
  });

  it("find is plain Ctrl+F (the chat view only binds it); next and previous are F3 and Shift+F3", () => {
    expect(matches("find", ev({ key: "f", ctrlKey: true }), undefined, "linux")).toBe(true);
    expect(matches("find", ev({ key: "F", ctrlKey: true, shiftKey: true }), undefined, "linux")).toBe(false);
    expect(matches("findNext", ev({ key: "F3" }), undefined, "linux")).toBe(true);
    expect(matches("findPrevious", ev({ key: "F3", shiftKey: true }), undefined, "linux")).toBe(true);
    expect(hotkeyLabel("findNext", undefined, "linux")).toBe("F3");
  });

  it("labels spell out modifiers", () => {
    expect(hotkeyLabel("expandEditor", undefined, "linux")).toBe("Ctrl+Shift+E");
    expect(hotkeyLabel("sendFromEditor", undefined, "linux")).toBe("Ctrl+⏎");
    expect(hotkeyLabel("attachFiles", undefined, "linux")).toBe("Ctrl+Shift+A");
    expect(hotkeyLabel("collapseEditor", undefined, "linux")).toBe("Esc");
  });
});

describe("overrides", () => {
  it("an override argument replaces the default for matching and labels", () => {
    const o = { expandEditor: { key: "k", mod: true, shift: true } };
    expect(matches("expandEditor", ev({ key: "K", ctrlKey: true, shiftKey: true }), o, "linux")).toBe(true);
    expect(matches("expandEditor", ev({ key: "E", ctrlKey: true, shiftKey: true }), o, "linux")).toBe(false);
    expect(hotkeyLabel("expandEditor", o, "darwin")).toBe("⌘⇧K");
  });

  it("setHotkeyOverrides applies globally and resets with {}", () => {
    setHotkeyOverrides({ attachFiles: { key: "u", mod: true, shift: false } });
    expect(matches("attachFiles", ev({ key: "u", ctrlKey: true }), undefined, "linux")).toBe(true);
    setHotkeyOverrides({});
    expect(matches("attachFiles", ev({ key: "u", ctrlKey: true }), undefined, "linux")).toBe(false);
  });
});
