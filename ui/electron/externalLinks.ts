/**
 * Where a URL the app window was asked to go to belongs: "allow" for the
 * app's own origin, "external" for any other http(s) URL (the user's
 * browser), "block" for anything else — mailto:, file:, javascript:, an
 * unparsable string. main.ts decides what each verdict means per event:
 * will-navigate keeps the window put even for "allow" (it has no back
 * button), will-redirect lets a same-origin redirect through.
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
