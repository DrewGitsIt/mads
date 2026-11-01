/**
 * Renderer-side accessor for opening links from rendered markdown — mirrors
 * ui/src/platform/directoryPicker.ts. In Electron, main decides what a link
 * is and how it opens (ui/electron/linkOpener.ts): web and localhost URLs in
 * the browser, files in the user's editor or default app. The `Window.mads.links`
 * shape is declared once in ui/src/conversation/TerminalRenderer/ptyBridge.ts.
 *
 * Outside Electron (a plain browser preview, vitest) there is no main to ask,
 * so http(s) opens in a new tab and nothing else opens at all — a file
 * answers "none".
 */

/** What became of a click (ui/electron/linkOpener.ts OpenOutcome). */
export type OpenOutcome = "opened" | "revealed" | "missing" | "none" | "cancelled" | "failed";
/** A link's kind for display; "missing" is a file link whose file isn't on this machine. */
export type LinkState = "web" | "localhost" | "file" | "missing" | "none";

export interface LinkOpener {
  /** `baseDir` is the session's folder, where a relative path resolves. */
  open(href: string, baseDir?: string): Promise<OpenOutcome>;
  /** A file the app already knows by absolute path — same editor, prompts and fallbacks as a file link. */
  openFile(path: string): Promise<OpenOutcome>;
  describe(hrefs: string[], baseDir?: string): Promise<LinkState[]>;
}

const isHttp = (href: string) => /^https?:\/\//i.test(href.trim());

const browserOpener: LinkOpener = {
  async open(href) {
    if (!isHttp(href)) return "none";
    window.open(href, "_blank", "noopener,noreferrer");
    return "opened";
  },
  async openFile() {
    return "none";
  },
  async describe(hrefs) {
    return hrefs.map((href) => (isHttp(href) ? "web" : "none"));
  },
};

export function getLinkOpener(): LinkOpener {
  const bridge = typeof window !== "undefined" ? window.mads?.links : undefined;
  if (!bridge) return browserOpener;
  return {
    open: (href, baseDir) => bridge.open(href, baseDir) as Promise<OpenOutcome>,
    openFile: (path) => bridge.openFile(path) as Promise<OpenOutcome>,
    describe: (hrefs, baseDir) => bridge.describe(hrefs, baseDir) as Promise<LinkState[]>,
  };
}

/** Hrefs that need main to say what they are: anything that isn't a web URL or an in-page anchor. */
export function needsDescribing(href: string): boolean {
  const raw = href.trim();
  return raw !== "" && !raw.startsWith("#") && !isHttp(raw);
}
