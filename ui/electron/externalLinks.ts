/**
 * What to do with a navigation of the app window: the app's own origin stays
 * in the window; any other http(s) URL belongs in the user's browser (the
 * window has no back button, so a link that navigated it would strand mads);
 * anything else — mailto:, file:, javascript:, an unparsable string — goes
 * nowhere.
 */
export type NavigationVerdict = "allow" | "external" | "block";

export function classifyNavigation(url: string, appUrl: string): NavigationVerdict {
  let target: URL;
  try {
    target = new URL(url);
  } catch {
    return "block";
  }
  if (target.origin === new URL(appUrl).origin) return "allow";
  return target.protocol === "http:" || target.protocol === "https:" ? "external" : "block";
}
