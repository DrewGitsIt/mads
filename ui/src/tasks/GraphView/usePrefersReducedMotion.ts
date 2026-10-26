import { useEffect, useState } from "react";

const QUERY = "(prefers-reduced-motion: reduce)";

/** The OS's setting, or the app's own (applyMotion's [data-motion="reduce"]), read now. */
export function prefersReducedMotion(): boolean {
  return (
    document.documentElement.dataset.motion === "reduce" || window.matchMedia?.(QUERY).matches === true
  );
}

export function usePrefersReducedMotion(): boolean {
  const [value, setValue] = useState(prefersReducedMotion);
  useEffect(() => {
    const mql = window.matchMedia(QUERY);
    const onChange = () => setValue(prefersReducedMotion());
    mql.addEventListener("change", onChange);
    const root = new MutationObserver(onChange);
    root.observe(document.documentElement, { attributes: true, attributeFilter: ["data-motion"] });
    return () => {
      mql.removeEventListener("change", onChange);
      root.disconnect();
    };
  }, []);
  return value;
}
