/**
 * The in-app "reduce motion" setting: [data-motion="reduce"] on the document
 * root, which every reduced-motion rule matches alongside the OS's
 * prefers-reduced-motion. Off, the root carries nothing and the OS decides.
 */
export function applyMotion(reduce: boolean | undefined): void {
  if (reduce) document.documentElement.dataset.motion = "reduce";
  else delete document.documentElement.dataset.motion;
}
