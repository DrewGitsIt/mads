// @vitest-environment jsdom
//
// DOMPurify's sanitization pass silently no-ops on some elements (observed:
// <script> survives) under happy-dom — a happy-dom/DOMPurify compatibility
// gap, not a DOMPurify bug. Every other test in this package runs under the
// project-wide happy-dom environment (see ui/README.md); this file alone
// overrides to jsdom so the sanitizer's security-critical behavior is
// actually exercised. Production renders in Electron's real Chromium DOM,
// where DOMPurify works as documented — this override only affects the test
// run, not runtime behavior.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { Markdown } from "./markdown";
import styles from "./markdown.module.css";

describe("Markdown", () => {
  it("renders basic markdown as HTML", () => {
    const { container } = render(<Markdown text="**bold** and `code`" />);
    expect(container.querySelector("strong")?.textContent).toBe("bold");
    expect(container.querySelector("code")?.textContent).toBe("code");
  });

  // Chat prose passes breaks; documents outside the chat (task
  // descriptions) keep markdown's soft wrap, the default.
  it("keeps a lone newline a soft wrap by default, and a line break with breaks", () => {
    const { container, rerender } = render(<Markdown text={"line one\nline two"} />);
    expect(container.querySelector("br")).toBeNull();
    rerender(<Markdown text={"line one\nline two"} breaks />);
    expect(container.querySelectorAll("br")).toHaveLength(1);
    expect(container.querySelectorAll("p")).toHaveLength(1);
  });

  it("leaves code blocks, lists and tables whole with breaks", () => {
    const text = [
      "Intro line\nsecond line",
      "",
      "- one\n- two\n  continued",
      "",
      "1. first\n2. second",
      "",
      "| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |",
      "",
      "```go\nfunc a() {\n\treturn\n}\n```",
      "",
      "    indented\n    code",
    ].join("\n");
    const { container } = render(<Markdown text={text} breaks />);
    // Code keeps its newlines as text, never as <br>.
    const codes = [...container.querySelectorAll("pre code")].map((c) => c.textContent);
    expect(codes).toEqual(["func a() {\n\treturn\n}\n", "indented\ncode\n"]);
    expect(container.querySelector("pre br")).toBeNull();
    // Lists keep their items; a continuation line breaks inside its item.
    expect(container.querySelectorAll("ul > li")).toHaveLength(2);
    expect(container.querySelectorAll("ol > li")).toHaveLength(2);
    expect(container.querySelectorAll("ul > li")[1].querySelectorAll("br")).toHaveLength(1);
    // Tables keep their rows and cells, with no breaks in them.
    expect(container.querySelectorAll("tbody tr")).toHaveLength(2);
    expect(container.querySelectorAll("td")).toHaveLength(4);
    expect(container.querySelector("table br")).toBeNull();
    // The intro paragraph breaks once, and only there.
    expect(container.querySelector("p")?.querySelectorAll("br")).toHaveLength(1);
  });

  it("strips script tags and event-handler attributes", () => {
    const { container } = render(
      <Markdown text={'<script>alert(1)</script><p onclick="alert(1)">hi</p>'} />,
    );
    expect(container.querySelector("script")).toBeNull();
    const p = container.querySelector("p");
    expect(p?.getAttribute("onclick")).toBeNull();
  });

  it("strips disallowed tags like img and iframe", () => {
    const { container } = render(
      <Markdown text={'<img src="x" onerror="alert(1)"><iframe src="evil"></iframe>'} />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("iframe")).toBeNull();
  });

  // Unstyled, a link falls back to the browser's #0000ee — near-unreadable on
  // the dark palettes. The wrapper carries the scoping class, and the rule
  // colors links from the palette-tuned --info-fg.
  it("scopes a palette-aware link color to rendered prose", () => {
    const { container } = render(
      <Markdown className="prose" text="see [the docs](https://example.com)" />,
    );
    const wrapper = container.firstElementChild!;
    expect(wrapper.classList.contains(styles.markdown)).toBe(true);
    expect(wrapper.classList.contains("prose")).toBe(true);
    expect(wrapper.querySelector("a")?.getAttribute("href")).toBe("https://example.com");

    const css = readFileSync("src/conversation/ChatRenderer/markdown.module.css", "utf8");
    expect(css).toMatch(/\.markdown a \{[^}]*color: var\(--info-fg\)/);
  });

  // Rendered block by block: a streaming reply only rebuilds the block still
  // growing, so a code block that lands mid-reply can fold in once.
  it("keeps finished blocks' DOM as the text grows", () => {
    const { container, rerender } = render(<Markdown text={"First paragraph.\n\nSecond"} />);
    const first = container.querySelector("p")!;
    rerender(<Markdown text={"First paragraph.\n\nSecond paragraph, growing"} />);
    expect(container.querySelector("p")).toBe(first);
    expect(container.querySelectorAll("p")[1].textContent).toBe("Second paragraph, growing");
  });

  it("resolves a reference link defined in a later block", () => {
    const { container } = render(<Markdown text={"See [the docs][d].\n\n[d]: https://example.com"} />);
    expect(container.querySelector("a")?.getAttribute("href")).toBe("https://example.com");
  });

  const foldOf = (pre: Element) => pre.closest("[data-open]")!;

  it("folds in a code block that arrives after mount when asked — never one already there", () => {
    const before = "Run this:\n\n```sh\nmake check\n```";
    const { container, rerender } = render(<Markdown text={before} foldNewCode />);
    expect(foldOf(container.querySelector("pre")!).hasAttribute("data-appear")).toBe(false);

    rerender(<Markdown text={`${before}\n\nThen:\n\n\`\`\`sh\nmake ship\n\`\`\``} foldNewCode />);
    const [, arrived] = container.querySelectorAll("pre");
    expect(foldOf(arrived).hasAttribute("data-appear")).toBe(true);
  });

  it("never folds code in when not asked (unpaced text grows its own blocks)", () => {
    const { container, rerender } = render(<Markdown text="Run this:" />);
    rerender(<Markdown text={"Run this:\n\n```sh\nmake check\n```"} />);
    expect(foldOf(container.querySelector("pre")!).hasAttribute("data-appear")).toBe(false);
  });

  it("lays blocks out as if they were one element (wrappers are display: contents)", () => {
    const { container } = render(<Markdown text={"a\n\nb"} />);
    expect(container.querySelector("p")!.parentElement!.classList.contains(styles.block)).toBe(true);
    const css = readFileSync("src/conversation/ChatRenderer/markdown.module.css", "utf8");
    expect(css).toMatch(/\.block \{[^}]*display: contents/);
  });
});

describe("Markdown code block Copy", () => {
  afterEach(() => {
    vi.useRealTimers();
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
  });

  function stubClipboard() {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    return writeText;
  }

  it("copies the block's source text, then reads Copied until it times out", async () => {
    vi.useFakeTimers();
    const writeText = stubClipboard();
    render(<Markdown text={"Run:\n\n```sh\necho '<hi>' && ls\n```\n"} />);

    fireEvent.click(screen.getByRole("button", { name: "Copy code" }));
    await act(async () => {});
    expect(writeText).toHaveBeenCalledWith("echo '<hi>' && ls");
    expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();

    act(() => vi.advanceTimersByTime(1500));
    expect(screen.getByRole("button", { name: "Copy code" })).toBeTruthy();
  });

  it("gives each top-level code block its own button, and inline code none", () => {
    render(<Markdown text={"`inline`\n\n```\none\n```\n\n```\ntwo\n```\n"} />);
    expect(screen.getAllByRole("button", { name: "Copy code" })).toHaveLength(2);
  });
});
