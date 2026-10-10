import { useEffect, useState } from "react";

const QUERY = "(prefers-reduced-motion: reduce)";

// The OS's setting, or the app's own (applyMotion's [data-motion="reduce"]).
function reduced(): boolean {
  return (
    document.documentElement.dataset.motion === "reduce" || window.matchMedia(QUERY).matches
  );
}

export function usePrefersReducedMotion(): boolean {
  const [value, setValue] = useState(reduced);
  useEffect(() => {
    const mql = window.matchMedia(QUERY);
    const onChange = () => setValue(reduced());
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
