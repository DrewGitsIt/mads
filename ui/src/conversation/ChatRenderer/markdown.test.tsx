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
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { Markdown } from "./markdown";
import styles from "./markdown.module.css";

describe("Markdown", () => {
  it("renders basic markdown as HTML", () => {
    const { container } = render(<Markdown text="**bold** and `code`" />);
    expect(container.querySelector("strong")?.textContent).toBe("bold");
    expect(container.querySelector("code")?.textContent).toBe("code");
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
